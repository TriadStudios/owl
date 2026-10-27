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
