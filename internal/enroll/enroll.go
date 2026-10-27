package enroll

import (
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/triadstudios/owl/internal/discovery"
	"github.com/triadstudios/owl/internal/perception"
)

type Status string

const (
	Verified Status = "verified"
	Inferred Status = "inferred"
	Unknown  Status = "unknown"
)

const (
	matchTolerance = 0.3
	swapMargin     = 0.15
	minExcessBytes = 512 << 10
	excessRatio    = 3.0
)

type Port struct {
	Num        int       `json:"num"`
	OnCamera   bool      `json:"on_camera"`
	Occupied   bool      `json:"occupied"`
	OperUp     bool      `json:"oper_up"`
	LinkOn     bool      `json:"link_on"`
	Energy     float64   `json:"activity_energy"`
	DeltaBytes uint64    `json:"delta_bytes"`
	MACs       []string  `json:"macs"`
	Neighbor   string    `json:"neighbor,omitempty"`
	Status     Status    `json:"status"`
	Reason     string    `json:"reason"`
	Source     string    `json:"source"`
	VerifiedAt time.Time `json:"verified_at,omitzero"`
	Window     int       `json:"window,omitempty"`
	BaseRate   float64   `json:"base_rate_bps"`
	BaseEnergy float64   `json:"base_activity_energy"`
	CableTo    string    `json:"cable_to,omitempty"`
}

type Baseline struct {
	Switch     string    `json:"switch"`
	FrameHash  string    `json:"frame_hash"`
	EnrolledAt time.Time `json:"enrolled_at"`
	Ports      []Port    `json:"ports"`
}

func Classify(cam *perception.Result, prev, cur *discovery.SwitchState, now time.Time) ([]Port, error) {
	if len(cam.LEDs) != len(cam.Ports) {
		return nil, fmt.Errorf("enroll needs LED readings for every camera port, got %d for %d", len(cam.LEDs), len(cam.Ports))
	}
	ports := make([]Port, len(cam.Ports))
	var candidates []int
	for i, c := range cam.Ports {
		p := Port{Num: c.Num, OnCamera: true, Occupied: c.Occupied, LinkOn: cam.LEDs[i].LinkOn, Energy: cam.LEDs[i].ActivityEnergy, Status: Unknown}
		after, okCur := cur.Ports[c.Num]
		before, okPrev := prev.Ports[c.Num]
		if !okCur || !okPrev {
			p.Reason = "port not on the switch"
			ports[i] = p
			continue
		}
		p.OperUp = after.OperUp
		p.MACs = after.MACs
		if after.LLDP != nil {
			p.Neighbor = after.LLDP.SysName
			if p.Neighbor == "" {
				p.Neighbor = after.LLDP.ChassisID
			}
		}
		p.DeltaBytes = (after.InOctets-before.InOctets)&0xffffffff + (after.OutOctets-before.OutOctets)&0xffffffff
		switch {
		case !p.Occupied && !p.OperUp && !p.LinkOn:
			p.Status, p.Source, p.VerifiedAt = Verified, "camera+snmp", now
			p.Reason = "no cable, link down, LED off"
		case !p.Occupied:
			p.Reason = fmt.Sprintf("no cable seen but oper up %t, LED on %t", p.OperUp, p.LinkOn)
		case !p.OperUp:
			p.Reason = "cable seen but link down"
		case !p.LinkOn:
			p.Reason = "link up but no LED seen"
		default:
			candidates = append(candidates, i)
		}
		ports[i] = p
	}

	onCamera := map[int]bool{}
	for _, c := range cam.Ports {
		onCamera[c.Num] = true
	}
	var unseen []int
	for n := range cur.Ports {
		if !onCamera[n] {
			unseen = append(unseen, n)
		}
	}
	slices.Sort(unseen)
	for _, n := range unseen {
		ports = append(ports, Port{Num: n, OperUp: cur.Ports[n].OperUp, MACs: cur.Ports[n].MACs, Status: Unknown, Reason: "camera cannot see this port"})
	}

	var maxEnergy, maxDelta float64
	for _, i := range candidates {
		maxEnergy = math.Max(maxEnergy, ports[i].Energy)
		maxDelta = math.Max(maxDelta, math.Log1p(float64(ports[i].DeltaBytes)))
	}
	norm := func(v, max float64) float64 {
		if max == 0 {
			return 0
		}
		return v / max
	}
	e := map[int]float64{}
	d := map[int]float64{}
	for _, i := range candidates {
		e[i] = norm(ports[i].Energy, maxEnergy)
		d[i] = norm(math.Log1p(float64(ports[i].DeltaBytes)), maxDelta)
	}
	for _, i := range candidates {
		fit := math.Abs(e[i] - d[i])
		if fit > matchTolerance {
			ports[i].Reason = fmt.Sprintf("LED activity %.2f vs counter activity %.2f disagree", e[i], d[i])
			continue
		}
		swapWith := 0
		for _, j := range candidates {
			if j != i && math.Abs(e[i]-d[j]) <= fit+swapMargin && math.Abs(e[j]-d[i]) <= math.Abs(e[j]-d[j])+swapMargin {
				swapWith = ports[j].Num
				break
			}
		}
		ports[i].Source = "camera+snmp"
		if swapWith == 0 {
			ports[i].Status, ports[i].VerifiedAt = Verified, now
			ports[i].Reason = fmt.Sprintf("LED activity %.2f matches counter activity %.2f, no other port fits", e[i], d[i])
		} else {
			ports[i].Status = Inferred
			ports[i].Reason = fmt.Sprintf("LED activity %.2f matches counter activity %.2f, but port %d fits as well", e[i], d[i], swapWith)
		}
	}
	return ports, nil
}

type Window struct {
	LEDs      []perception.LED
	Prev, Cur *discovery.SwitchState
}

func Best(cam *perception.Result, windows []Window, now time.Time) ([]Port, error) {
	rank := map[Status]int{Unknown: 0, Inferred: 1, Verified: 2}
	var best []Port
	var rate, energy []float64
	for w, win := range windows {
		frame := *cam
		frame.LEDs = win.LEDs
		ports, err := Classify(&frame, win.Prev, win.Cur, now)
		if err != nil {
			return nil, err
		}
		seconds := win.Cur.Polled.Sub(win.Prev.Polled).Seconds()
		if seconds <= 0 {
			return nil, fmt.Errorf("window %d has no duration", w+1)
		}
		if best == nil {
			best, rate, energy = make([]Port, len(ports)), make([]float64, len(ports)), make([]float64, len(ports))
		}
		for i, p := range ports {
			p.Window = w + 1
			rate[i] += float64(p.DeltaBytes) / seconds
			energy[i] += p.Energy
			if w == 0 || rank[p.Status] > rank[best[i].Status] {
				best[i] = p
			}
		}
	}
	for i := range best {
		best[i].BaseRate = rate[i] / float64(len(windows))
		best[i].BaseEnergy = energy[i] / float64(len(windows))
	}
	return best, nil
}

type Probe struct {
	Target    int
	LEDs      []perception.LED
	Prev, Cur *discovery.SwitchState
}

func Induced(ports []Port, probe Probe, others []int) (bool, string) {
	seconds := probe.Cur.Polled.Sub(probe.Prev.Polled).Seconds()
	measure := func(num int) (excess, rise float64) {
		after, before := probe.Cur.Ports[num], probe.Prev.Ports[num]
		if after == nil || before == nil {
			return 0, 0
		}
		delta := (after.InOctets-before.InOctets)&0xffffffff + (after.OutOctets-before.OutOctets)&0xffffffff
		for _, p := range ports {
			if p.Num == num {
				excess = float64(delta) - p.BaseRate*seconds
				rise = -p.BaseEnergy
			}
		}
		for _, l := range probe.LEDs {
			if l.Num == num {
				rise += l.ActivityEnergy
			}
		}
		return excess, rise
	}
	excess, rise := measure(probe.Target)
	var otherExcess, otherRise float64
	for _, n := range others {
		e, r := measure(n)
		otherExcess, otherRise = math.Max(otherExcess, e), math.Max(otherRise, r)
	}
	ok := excess >= minExcessBytes && excess >= excessRatio*otherExcess && rise > 0 && rise >= otherRise
	return ok, fmt.Sprintf("ping spike on port %d: +%.0f KiB, LED +%.2f; largest elsewhere +%.0f KiB, LED +%.2f",
		probe.Target, excess/1024, rise, otherExcess/1024, otherRise)
}
