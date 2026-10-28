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
	matchTolerance      = 0.3
	swapMargin          = 0.15
	pulseMinCorrelation = 0.8
	pulseMaxOther       = 0.4
	pulseStep           = 50 * time.Millisecond
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

type Pulse struct {
	Start   time.Time
	On, Off time.Duration
	Cycles  int
	Samples []discovery.Sample
}

func (p Pulse) on(t time.Time) bool {
	since := t.Sub(p.Start)
	period := p.On + p.Off
	return since >= 0 && since < time.Duration(p.Cycles)*period && since%period < p.On
}

// Correlate gives the Pearson correlation of a port's per-interval in and out byte rates with the share of each interval the ping was on.
func (p Pulse) Correlate(num int) (in, out float64) {
	var pattern, rateIn, rateOut []float64
	for k := 1; k < len(p.Samples); k++ {
		a, b := p.Samples[k-1], p.Samples[k]
		seconds := b.At.Sub(a.At).Seconds()
		if seconds <= 0 {
			continue
		}
		var on, steps float64
		for t := a.At; t.Before(b.At); t = t.Add(pulseStep) {
			steps++
			if p.on(t) {
				on++
			}
		}
		pattern = append(pattern, on/steps)
		rateIn = append(rateIn, float64((b.In[num]-a.In[num])&0xffffffff)/seconds)
		rateOut = append(rateOut, float64((b.Out[num]-a.Out[num])&0xffffffff)/seconds)
	}
	return pearson(pattern, rateIn), pearson(pattern, rateOut)
}

func pearson(x, y []float64) float64 {
	n := float64(len(x))
	if n < 3 {
		return 0
	}
	var mx, my float64
	for i := range x {
		mx, my = mx+x[i]/n, my+y[i]/n
	}
	var sxy, sxx, syy float64
	for i := range x {
		sxy += (x[i] - mx) * (y[i] - my)
		sxx += (x[i] - mx) * (x[i] - mx)
		syy += (y[i] - my) * (y[i] - my)
	}
	if sxx == 0 || syy == 0 {
		return 0
	}
	return sxy / math.Sqrt(sxx*syy)
}

func Pulsed(p Pulse, target int, others []int) (bool, string) {
	in, out := p.Correlate(target)
	other, otherPort := math.Inf(-1), 0
	for _, n := range others {
		oi, oo := p.Correlate(n)
		if m := math.Max(oi, oo); m > other {
			other, otherPort = m, n
		}
	}
	elsewhere := "no other lit port"
	if otherPort != 0 {
		elsewhere = fmt.Sprintf("largest elsewhere r=%.2f on port %d", other, otherPort)
	}
	ok := in >= pulseMinCorrelation && out >= pulseMinCorrelation && (otherPort == 0 || other < pulseMaxOther)
	return ok, fmt.Sprintf("ping pulsed %s on/%s off x%d: port %d in r=%.2f, out r=%.2f (need >= %.1f); %s (need < %.1f)",
		p.On, p.Off, p.Cycles, target, in, out, pulseMinCorrelation, elsewhere, pulseMaxOther)
}
