package guardian

import (
	"math"
	"slices"
	"time"
)

const (
	approachFrames   = 5
	approachSpan     = 1500 * time.Millisecond
	stableFrames     = 3
	warnWidths       = 2.0
	lookahead        = 700 * time.Millisecond
	minApproachSpeed = 0.5
	withdrawAfter    = time.Second
)

type EventKind string

const (
	HandSeen     EventKind = "hand first seen"
	TargetLocked EventKind = "target locked"
	Stop         EventKind = "stop"
	Deviation    EventKind = "deviation"
	Expected     EventKind = "expected"
	Withdrawn    EventKind = "hand withdrawn"
)

type PortBox struct {
	Num    int
	X0, X1 float64
}

type HandObs struct {
	T   time.Time
	Box [4]float64
}

type ApproachEvent struct {
	Kind       EventKind
	Port       int
	JobPort    int
	JobID      int
	Confidence float64
	Distance   float64
}

type Approach struct {
	Ports      []PortBox
	RowY0      float64
	RowY1      float64
	DoNotTouch map[int]bool
	Populated  map[int]bool
	JobPorts   map[int]int

	history  []HandObs
	targets  []int
	present  bool
	lastSeen time.Time
	locked   int
	decided  map[int]bool
}

func (a *Approach) Update(o *HandObs, now time.Time) []ApproachEvent {
	if o == nil {
		if a.present && now.Sub(a.lastSeen) >= withdrawAfter {
			a.present, a.history, a.targets, a.locked, a.decided = false, nil, nil, 0, nil
			return []ApproachEvent{{Kind: Withdrawn}}
		}
		return nil
	}
	var events []ApproachEvent
	if !a.present {
		a.present, a.decided = true, map[int]bool{}
		events = append(events, ApproachEvent{Kind: HandSeen})
	}
	a.lastSeen = o.T
	a.history = append(a.history, *o)
	for len(a.history) > approachFrames || o.T.Sub(a.history[0].T) > approachSpan {
		a.history = a.history[1:]
	}
	x, dist := a.Project(a.history)
	target := a.PortAt(x)
	a.targets = append(a.targets, target)
	if len(a.targets) > stableFrames {
		a.targets = a.targets[len(a.targets)-stableFrames:]
	}
	agree := 0
	for _, t := range a.targets {
		if t == target {
			agree++
		}
	}
	width := a.portWidth()
	confidence := math.Max(0, 1-dist/(warnWidths*width)) * float64(agree) / stableFrames
	if target == 0 || agree < stableFrames {
		return events
	}
	if target != a.locked {
		a.locked = target
		events = append(events, ApproachEvent{Kind: TargetLocked, Port: target, Confidence: confidence, Distance: dist})
	}
	if dist > warnWidths*width || a.decided[target] {
		return events
	}
	a.decided[target] = true
	e := ApproachEvent{Port: target, Confidence: confidence, Distance: dist}
	if id, ok := a.JobPorts[target]; ok {
		e.Kind, e.JobID = Expected, id
		return append(events, e)
	}
	for port, id := range a.JobPorts {
		if e.JobPort == 0 || port < e.JobPort {
			e.JobPort, e.JobID = port, id
		}
	}
	switch {
	case len(a.JobPorts) > 0 && (a.Populated[target] || a.DoNotTouch[target]):
		e.Kind = Deviation
	case a.DoNotTouch[target]:
		e.Kind = Stop
	default:
		return events
	}
	return append(events, e)
}

func (a *Approach) lead(b [4]float64) (float64, float64) {
	x := (b[0] + b[2]) / 2
	switch {
	case b[1] > a.RowY1:
		return x, b[1]
	case b[3] < a.RowY0:
		return x, b[3]
	default:
		return x, (a.RowY0 + a.RowY1) / 2
	}
}

func (a *Approach) gap(y float64) float64 {
	return math.Max(0, math.Max(y-a.RowY1, a.RowY0-y))
}

func (a *Approach) Project(track []HandObs) (float64, float64) {
	last := track[len(track)-1]
	x, y := a.lead(last.Box)
	dist := a.gap(y)
	if len(track) < 2 || dist == 0 {
		return x, dist
	}
	var st, sx, sy, stt, stx, sty float64
	n := float64(len(track))
	for _, o := range track {
		t := o.T.Sub(track[0].T).Seconds()
		ox, oy := a.lead(o.Box)
		st, sx, sy, stt, stx, sty = st+t, sx+ox, sy+oy, stt+t*t, stx+t*ox, sty+t*oy
	}
	den := n*stt - st*st
	if den == 0 {
		return x, dist
	}
	vx, vy := (n*stx-st*sx)/den, (n*sty-st*sy)/den
	toward := -vy
	if y < a.RowY0 {
		toward = vy
	}
	if toward < minApproachSpeed*a.portWidth() {
		return x, dist
	}
	ahead := math.Min(dist/toward, lookahead.Seconds())
	return x + vx*ahead, dist
}

func (a *Approach) PortAt(x float64) int {
	for i, p := range a.Ports {
		left, right := p.X0-(p.X1-p.X0)/2, p.X1+(p.X1-p.X0)/2
		if i > 0 {
			left = (a.Ports[i-1].X1 + p.X0) / 2
		}
		if i < len(a.Ports)-1 {
			right = (p.X1 + a.Ports[i+1].X0) / 2
		}
		if x >= left && x < right {
			return p.Num
		}
	}
	return 0
}

func (a *Approach) portWidth() float64 {
	widths := make([]float64, len(a.Ports))
	for i, p := range a.Ports {
		widths[i] = p.X1 - p.X0
	}
	slices.Sort(widths)
	if len(widths) == 0 {
		return 1
	}
	return widths[len(widths)/2]
}
