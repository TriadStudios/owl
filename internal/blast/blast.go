package blast

import (
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/triadstudios/owl/internal/discovery"
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
	Inputs      []string
}

func Assess(num int, prev, cur *discovery.SwitchState, ids *discovery.Identities, rules policy.Rules) (Card, error) {
	now, ok := cur.Ports[num]
	if !ok {
		return Card{}, fmt.Errorf("port %d not on the switch", num)
	}
	before, ok := prev.Ports[num]
	if !ok {
		return Card{}, fmt.Errorf("port %d missing from the earlier poll", num)
	}
	card := Card{Port: num, Behind: len(now.MACs), Window: cur.Polled.Sub(prev.Polled)}
	for _, mac := range now.MACs {
		card.Devices = append(card.Devices, ids.Lookup(mac))
	}

	var caps []string
	if now.LLDP != nil {
		card.Neighbor = now.LLDP.SysName
		if card.Neighbor == "" {
			card.Neighbor = now.LLDP.ChassisID
		}
		caps = now.LLDP.Capabilities
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
	if card.DeltaBytes >= rules.LivenessActiveBytes {
		card.Liveness = Active
	}
	card.Inputs = append(card.Inputs, fmt.Sprintf("in+out %d B over %s vs liveness_active_bytes %d -> %s (snmp)", card.DeltaBytes, card.Window.Round(time.Second), rules.LivenessActiveBytes, card.Liveness))

	var uplinkBy []string
	for _, c := range caps {
		if slices.Contains(rules.UplinkCapabilities, c) {
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
		for _, pattern := range rules.CriticalNames {
			if ok, _ := path.Match(pattern, strings.ToLower(name)); ok && name != "" {
				matched = append(matched, fmt.Sprintf("%s ~ %s", name, pattern))
			}
		}
	}
	if len(matched) > 0 {
		card.Criticality = append(card.Criticality, "critical")
		card.Inputs = append(card.Inputs, "critical_names match: "+strings.Join(matched, ", "))
	} else {
		card.Inputs = append(card.Inputs, fmt.Sprintf("no name matches critical_names %v", rules.CriticalNames))
	}

	switch {
	case len(card.Criticality) > 0:
		card.Verdict = DoNotTouch
	case card.Liveness == Active || card.Behind > 1:
		card.Verdict = Caution
	default:
		card.Verdict = Safe
	}
	return card, nil
}
