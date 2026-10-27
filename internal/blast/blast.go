package blast

import (
	"fmt"
	"net"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/triadstudios/owl/internal/discovery"
	"github.com/triadstudios/owl/internal/perception"
	"github.com/triadstudios/owl/internal/policy"
)

type Liveness string

const (
	Idle   Liveness = "idle"
	Active Liveness = "active"
)

type Verdict string

const (
	Safe       Verdict = "safe"
	Caution    Verdict = "caution"
	DoNotTouch Verdict = "do-not-touch"
)

type Tagger interface {
	Tags(mac string) (tags []string, found bool, err error)
}

type Inputs struct {
	Prev, Cur      *discovery.SwitchState
	Camera         *perception.Result
	AttributionMin float64
	AggregationMin int
	IDs            *discovery.Identities
	Tags           Tagger
	Rules          policy.Rules
}

type Card struct {
	Port        int
	Devices     []discovery.Device
	Behind      int
	Neighbor    string
	Downstream  string
	DeltaBytes  uint64
	Window      time.Duration
	Liveness    Liveness
	Criticality []string
	Verdict     Verdict
	Unknown     string
	Inputs      []string
}

func Assess(num int, in Inputs) (Card, error) {
	now, ok := in.Cur.Ports[num]
	if !ok {
		return Card{}, fmt.Errorf("port %d not on the switch", num)
	}
	before, ok := in.Prev.Ports[num]
	if !ok {
		return Card{}, fmt.Errorf("port %d missing from the earlier poll", num)
	}
	card := Card{Port: num, Behind: len(now.MACs), Window: in.Cur.Polled.Sub(in.Prev.Polled)}
	for _, mac := range now.MACs {
		card.Devices = append(card.Devices, in.IDs.Lookup(mac))
	}

	camIndex := slices.IndexFunc(in.Camera.Ports, func(p perception.Port) bool { return p.Num == num })
	switch {
	case camIndex < 0:
		card.Unknown = fmt.Sprintf("camera cannot see port %d", num)
	case in.Camera.Ports[camIndex].Confidence == nil:
		card.Unknown = "camera attribution has no confidence"
	case *in.Camera.Ports[camIndex].Confidence < in.AttributionMin:
		card.Unknown = fmt.Sprintf("camera attribution confidence %.2f < OWL_ATTRIBUTION_MIN %.2f", *in.Camera.Ports[camIndex].Confidence, in.AttributionMin)
	default:
		card.Inputs = append(card.Inputs, fmt.Sprintf("camera sees port %d, confidence %.2f >= %.2f (camera)", num, *in.Camera.Ports[camIndex].Confidence, in.AttributionMin))
	}

	var caps []string
	macs := slices.Clone(now.MACs)
	if now.LLDP != nil {
		card.Neighbor = now.LLDP.SysName
		if card.Neighbor == "" {
			card.Neighbor = now.LLDP.ChassisID
		}
		caps = now.LLDP.Capabilities
		for _, id := range []string{now.LLDP.PortID, now.LLDP.ChassisID} {
			if _, err := net.ParseMAC(id); err == nil && !slices.Contains(macs, id) {
				macs = append(macs, id)
			}
		}
	}
	switch {
	case card.Behind > 1:
		var named []string
		for _, d := range card.Devices {
			if d.Hostname != "" && d.Hostname != card.Neighbor {
				named = append(named, d.Hostname)
			}
		}
		slices.Sort(named)
		card.Downstream = strings.TrimSpace(fmt.Sprintf("%d devices behind %s", card.Behind, card.Neighbor))
		if len(named) > 0 {
			card.Downstream += ": " + strings.Join(named, ", ")
		}
	case card.Behind == 1 && card.Devices[0].Hostname != "":
		card.Downstream = card.Devices[0].Hostname
	case card.Neighbor != "":
		card.Downstream = card.Neighbor
	case card.Behind == 1:
		card.Downstream = strings.TrimSpace(card.Devices[0].Vendor + " " + card.Devices[0].MAC)
	}
	card.Inputs = append(card.Inputs, fmt.Sprintf("%d MAC(s) on the port (snmp fdb)", card.Behind))

	// Counter32 wraps at 2^32; an agent reset also reads as a large delta, which errs toward caution.
	card.DeltaBytes = (now.InOctets-before.InOctets)&0xffffffff + (now.OutOctets-before.OutOctets)&0xffffffff
	card.Liveness = Idle
	if card.DeltaBytes >= in.Rules.LivenessActiveBytes {
		card.Liveness = Active
	}
	card.Inputs = append(card.Inputs, fmt.Sprintf("in+out %d B over %s vs liveness_active_bytes %d -> %s (snmp)", card.DeltaBytes, card.Window.Round(time.Second), in.Rules.LivenessActiveBytes, card.Liveness))

	tagged, netboxNote := false, "netbox not configured"
	if in.Tags != nil {
		netboxNote = "netbox has no device for these MACs"
		var hits []string
		for _, mac := range macs {
			tags, found, err := in.Tags.Tags(mac)
			if err != nil {
				tagged, hits, netboxNote = false, nil, "netbox unavailable: "+err.Error()
				break
			}
			if !found {
				continue
			}
			tagged = true
			for _, t := range tags {
				switch t {
				case "uplink":
					card.Criticality = append(card.Criticality, "uplink")
				case "critical", "backup-target":
					card.Criticality = append(card.Criticality, "critical")
				default:
					continue
				}
				hits = append(hits, mac+" tagged "+t)
			}
		}
		if tagged {
			slices.Sort(card.Criticality)
			card.Criticality = slices.Compact(card.Criticality)
			if len(hits) > 0 {
				card.Inputs = append(card.Inputs, "netbox tags: "+strings.Join(hits, ", ")+" (netbox)")
			} else {
				card.Inputs = append(card.Inputs, "netbox knows the port's devices; none tagged uplink, critical or backup-target (netbox)")
			}
		} else {
			card.Criticality = nil
		}
	}
	if !tagged {
		card.Inputs = append(card.Inputs, netboxNote+"; criticality from policy/rules.yaml")
		var uplinkBy []string
		for _, c := range caps {
			if slices.Contains(in.Rules.UplinkCapabilities, c) {
				uplinkBy = append(uplinkBy, c)
			}
		}
		if len(uplinkBy) > 0 {
			card.Criticality = append(card.Criticality, "uplink")
			card.Inputs = append(card.Inputs, fmt.Sprintf("LLDP neighbor %s advertises %s, in uplink_capabilities (lldp)", card.Neighbor, strings.Join(uplinkBy, ", ")))
		} else {
			card.Inputs = append(card.Inputs, fmt.Sprintf("no LLDP neighbor capability in uplink_capabilities (neighbor %q, capabilities %v)", card.Neighbor, caps))
		}

		names := []string{card.Neighbor}
		for _, d := range card.Devices {
			names = append(names, d.Hostname)
		}
		var matched []string
		for _, name := range names {
			for _, pattern := range in.Rules.CriticalNames {
				if ok, _ := path.Match(pattern, strings.ToLower(name)); ok && name != "" {
					matched = append(matched, fmt.Sprintf("%s ~ %s", name, pattern))
				}
			}
		}
		if len(matched) > 0 {
			card.Criticality = append(card.Criticality, "critical")
			card.Inputs = append(card.Inputs, "critical_names match: "+strings.Join(matched, ", "))
		} else {
			card.Inputs = append(card.Inputs, fmt.Sprintf("no name matches critical_names %v", in.Rules.CriticalNames))
		}
	}

	var aggregation string
	switch {
	case card.Behind > in.AggregationMin:
		aggregation = fmt.Sprintf("%d MACs behind the port > OWL_AGGREGATION_MIN %d (snmp fdb)", card.Behind, in.AggregationMin)
	case slices.Contains(caps, "wlan-access-point") || slices.Contains(caps, "bridge"):
		aggregation = fmt.Sprintf("LLDP neighbor %s advertises %v, an AP or switch (lldp)", card.Neighbor, caps)
	}
	if aggregation != "" {
		card.Criticality = append(card.Criticality, "aggregation")
		card.Inputs = append(card.Inputs, "aggregation: "+aggregation)
	} else {
		card.Inputs = append(card.Inputs, fmt.Sprintf("not aggregation: %d MAC(s) <= OWL_AGGREGATION_MIN %d and no AP or switch neighbor", card.Behind, in.AggregationMin))
	}

	switch {
	case card.Unknown != "":
	case len(card.Criticality) > 0:
		card.Verdict = DoNotTouch
	case card.Liveness == Active || card.Behind > 1:
		card.Verdict = Caution
	default:
		card.Verdict = Safe
	}
	return card, nil
}
