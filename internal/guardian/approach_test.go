package guardian

import (
	"testing"
	"time"
)

func rack() *Approach {
	var ports []PortBox
	for i := range 8 {
		x0 := 600 + float64(i)*85
		ports = append(ports, PortBox{Num: i + 1, X0: x0, X1: x0 + 72})
	}
	return &Approach{
		Ports:      ports,
		RowY0:      200,
		RowY1:      250,
		DoNotTouch: map[int]bool{1: true, 2: true},
		Populated:  map[int]bool{1: true, 2: true, 3: true, 7: true},
	}
}

func center(a *Approach, port int) float64 {
	p := a.Ports[port-1]
	return (p.X0 + p.X1) / 2
}

type step struct {
	x, top float64
}

func run(a *Approach, steps []step) ([]ApproachEvent, []int) {
	t0 := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	var events []ApproachEvent
	var targets []int
	for i, s := range steps {
		t := t0.Add(time.Duration(i) * 100 * time.Millisecond)
		events = append(events, a.Update(&HandObs{T: t, Box: [4]float64{s.x - 40, s.top, s.x + 40, s.top + 120}}, t)...)
		targets = append(targets, a.PortAt(func() float64 { x, _ := a.Project(a.history); return x }()))
	}
	return events, targets
}

func kinds(events []ApproachEvent) []EventKind {
	var out []EventKind
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

func TestStraightReachForAnUplinkPortStopsOnceWithinTwoWidths(t *testing.T) {
	a := rack()
	var steps []step
	for i := range 10 {
		steps = append(steps, step{center(a, 2), 600 - float64(i)*40})
	}
	events, _ := run(a, steps)
	var stops []ApproachEvent
	for i, e := range events {
		if e.Kind == Stop {
			stops = append(stops, e)
			if e.Distance > 2*72 {
				t.Errorf("stop %d fired %.0f px from the row, more than two port widths", i, e.Distance)
			}
		}
	}
	if len(stops) != 1 || stops[0].Port != 2 {
		t.Fatalf("events %v, want exactly one stop on port 2", kinds(events))
	}
	if events[0].Kind != HandSeen {
		t.Errorf("first event %s, want %s", events[0].Kind, HandSeen)
	}
}

func TestDiagonalReachProjectsToTheTargetBeforeTheHandIsOverIt(t *testing.T) {
	a := rack()
	start, end := center(a, 4), center(a, 2)
	var steps []step
	for i := range 5 {
		f := float64(i) / 6
		steps = append(steps, step{start + (end-start)*f, 520 - 45*float64(i)})
	}
	_, targets := run(a, steps)
	last := steps[len(steps)-1]
	if a.PortAt(last.x) == 2 {
		t.Fatalf("test setup: hand already over port 2 at x %.0f", last.x)
	}
	if targets[len(targets)-1] != 2 {
		t.Fatalf("projected targets %v while the hand is over port %d, want port 2", targets, a.PortAt(last.x))
	}
}

func TestReachForASafePortLocksWithoutWarning(t *testing.T) {
	a := rack()
	var steps []step
	for i := range 10 {
		steps = append(steps, step{center(a, 4), 600 - float64(i)*40})
	}
	events, _ := run(a, steps)
	for _, e := range events {
		if e.Kind == Stop || e.Kind == Deviation {
			t.Fatalf("events %v, want no warning for safe port 4", kinds(events))
		}
	}
	if len(events) < 2 || events[1].Kind != TargetLocked || events[1].Port != 4 {
		t.Fatalf("events %+v, want port 4 locked", events)
	}
}

func TestUnstableProjectionNeverWarns(t *testing.T) {
	a := rack()
	var steps []step
	for i := range 12 {
		port := 2
		if i%2 == 1 {
			port = 3
		}
		steps = append(steps, step{center(a, port), 330 - float64(i)*5})
	}
	events, _ := run(a, steps)
	for _, e := range events {
		if e.Kind == Stop || e.Kind == TargetLocked {
			t.Fatalf("events %v, want nothing locked while the projection jumps between ports 2 and 3", kinds(events))
		}
	}
}

func TestHoveringFarFromTheRowLocksButDoesNotWarnUntilClose(t *testing.T) {
	a := rack()
	steps := []step{{center(a, 2), 600}, {center(a, 2), 600}, {center(a, 2), 600}, {center(a, 2), 600}}
	events, _ := run(a, steps)
	if got := kinds(events); len(got) != 2 || got[1] != TargetLocked {
		t.Fatalf("events %v, want seen and locked only while 350 px away", got)
	}
	t0 := time.Date(2026, 10, 28, 12, 0, 1, 0, time.UTC)
	var close []ApproachEvent
	for i := range 3 {
		tt := t0.Add(time.Duration(i) * 100 * time.Millisecond)
		close = append(close, a.Update(&HandObs{T: tt, Box: [4]float64{center(a, 2) - 40, 300, center(a, 2) + 40, 420}}, tt)...)
	}
	if got := kinds(close); len(got) != 1 || got[0] != Stop {
		t.Fatalf("events %v once 50 px away, want one stop", got)
	}
}

func TestJobsMakeTheirPortExpectedAndOthersDeviations(t *testing.T) {
	cases := []struct {
		port int
		want EventKind
	}{
		{2, Expected},
		{3, Deviation},
		{1, Deviation},
		{5, ""},
	}
	for _, c := range cases {
		a := rack()
		a.JobPorts = map[int]int{2: 7}
		var steps []step
		for i := range 10 {
			steps = append(steps, step{center(a, c.port), 600 - float64(i)*40})
		}
		events, _ := run(a, steps)
		var got EventKind
		var ev ApproachEvent
		for _, e := range events {
			if e.Kind == Expected || e.Kind == Deviation || e.Kind == Stop {
				got, ev = e.Kind, e
			}
		}
		if got != c.want {
			t.Errorf("job on port 2, reach for port %d: %v, want %q", c.port, kinds(events), c.want)
		}
		if got == Deviation && (ev.JobPort != 2 || ev.JobID != 7) {
			t.Errorf("deviation on port %d names job port %d id %d, want 2 and 7", c.port, ev.JobPort, ev.JobID)
		}
	}
}

func TestWithdrawnAfterASecondWithoutAHandAndANewReachCanWarnAgain(t *testing.T) {
	a := rack()
	var steps []step
	for i := range 10 {
		steps = append(steps, step{center(a, 2), 600 - float64(i)*40})
	}
	run(a, steps)
	t0 := time.Date(2026, 10, 28, 12, 0, 0, 900_000_000, time.UTC)
	if e := a.Update(nil, t0.Add(500*time.Millisecond)); len(e) != 0 {
		t.Fatalf("withdrawn after 0.5 s: %v", kinds(e))
	}
	if e := a.Update(nil, t0.Add(1100*time.Millisecond)); len(e) != 1 || e[0].Kind != Withdrawn {
		t.Fatalf("events %v after 1.1 s without a hand, want withdrawn", kinds(e))
	}
	events, _ := run(a, steps)
	stops := 0
	for _, e := range events {
		if e.Kind == Stop {
			stops++
		}
	}
	if stops != 1 {
		t.Fatalf("second reach: events %v, want one new stop", kinds(events))
	}
}
