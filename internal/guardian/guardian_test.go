package guardian

import (
	"strings"
	"testing"
	"time"

	"github.com/triadstudios/owl/internal/blast"
	"github.com/triadstudios/owl/internal/discovery"
	"github.com/triadstudios/owl/internal/reconcile"
)

type fakeRobot struct {
	homes   int
	turns   []Point
	said    []string
	replies []string
}

func (r *fakeRobot) Turn(to Point)   { r.turns = append(r.turns, to) }
func (r *fakeRobot) Home()           { r.homes++ }
func (r *fakeRobot) Say(text string) { r.said = append(r.said, text) }
func (r *fakeRobot) Listen() string {
	if len(r.replies) == 0 {
		return ""
	}
	reply := r.replies[0]
	r.replies = r.replies[1:]
	return reply
}

type fakeCamera struct {
	moving   bool
	centroid Point
}

func (c *fakeCamera) Frame() Frame { return Frame{Centroid: c.centroid} }
func (c *fakeCamera) Motion() bool { return c.moving }

func table() []reconcile.Port {
	return []reconcile.Port{
		{Num: 1, Camera: "occupied", Blast: &blast.Card{Verdict: blast.DoNotTouch, Neighbor: "Deco-AP", Behind: 7, Criticality: []string{"aggregation"}, Downstream: "7 devices behind Deco-AP: tv"}},
		{Num: 2, Camera: "occupied", Blast: &blast.Card{Verdict: blast.DoNotTouch, Neighbor: "GS-BE18000", Behind: 23, Criticality: []string{"uplink"}, Downstream: "23 devices behind GS-BE18000"}},
		{Num: 3, Camera: "occupied", Blast: &blast.Card{Verdict: blast.Safe, Behind: 1, Liveness: blast.Idle, Downstream: "NAS-01", Devices: []discovery.Device{{Hostname: "NAS-01"}}}},
		{Num: 4, Camera: "empty", Blast: &blast.Card{Unknown: "camera attribution confidence 0.40 < OWL_ATTRIBUTION_MIN 0.60"}},
		{Num: 9, Blast: &blast.Card{Verdict: blast.Safe}},
	}
}

func awake(state State, robot *fakeRobot, cam *fakeCamera, now time.Time) *Guardian {
	return &Guardian{Robot: robot, Camera: cam, Table: table, Colors: map[int]string{2: "blue"}, state: state, lastMotion: now}
}

func TestIdleWakesOnOneSecondOfMotionAndTurnsToIt(t *testing.T) {
	robot, cam := &fakeRobot{}, &fakeCamera{centroid: Point{0.2, 0.7}}
	g := &Guardian{Robot: robot, Camera: cam, Table: table}
	t0 := time.Now()
	cam.moving = true
	g.Step(t0)
	cam.moving = false
	g.Step(t0.Add(500 * time.Millisecond))
	cam.moving = true
	g.Step(t0.Add(600 * time.Millisecond))
	g.Step(t0.Add(1500 * time.Millisecond))
	if g.State() != Idle {
		t.Fatalf("woke after motion broken by a still frame: state %s", g.State())
	}
	g.Step(t0.Add(1600 * time.Millisecond))
	if g.State() != Wake || len(robot.turns) != 1 || robot.turns[0] != cam.centroid {
		t.Fatalf("state %s turns %v, want wake and one turn to %v", g.State(), robot.turns, cam.centroid)
	}
}

func TestWakeGreetsAsksAndListens(t *testing.T) {
	now := time.Now()
	robot, cam := &fakeRobot{replies: []string{"I'm pulling port 3"}}, &fakeCamera{}
	g := awake(Wake, robot, cam, now)
	g.Step(now.Add(time.Second))
	if g.State() != Ask || len(robot.said) != 1 || !strings.Contains(robot.said[0], "What are you here for?") || g.heard != "I'm pulling port 3" {
		t.Fatalf("state %s said %q heard %q", g.State(), robot.said, g.heard)
	}
}

func TestAskSpeaksTheBlastCard(t *testing.T) {
	cases := []struct {
		heard string
		want  []string
	}{
		{"I'm pulling port 2", []string{"Port 2, 2nd from the left, blue cable", "Do not touch: uplink, GS-BE18000", "23 devices behind it"}},
		{"unplugging the nas 01", []string{"Port 3, 3rd from the left", "NAS-01", "Safe"}},
		{"port 4", []string{"Port 4: I can't tell", "confidence 0.40"}},
		{"port 9", []string{"Port 9: I can't tell", "camera cannot see port 9"}},
		{"port 12", []string{"Port 12: I can't tell", "not on the switch"}},
	}
	for _, c := range cases {
		now := time.Now()
		robot, cam := &fakeRobot{}, &fakeCamera{}
		g := awake(Ask, robot, cam, now)
		g.heard = c.heard
		g.Step(now.Add(time.Second))
		if g.State() != Answer || len(robot.said) != 1 {
			t.Fatalf("%q: state %s said %q, want answer and one line", c.heard, g.State(), robot.said)
		}
		for _, w := range c.want {
			if !strings.Contains(robot.said[0], w) {
				t.Errorf("%q: said %q, missing %q", c.heard, robot.said[0], w)
			}
		}
	}

	now := time.Now()
	robot := &fakeRobot{replies: []string{"port 3"}}
	g := awake(Ask, robot, &fakeCamera{}, now)
	g.heard = "just looking"
	g.Step(now.Add(time.Second))
	if g.State() != Ask || g.heard != "port 3" || robot.said[0] != missLine {
		t.Fatalf("unparsed utterance: state %s said %q heard %q, want a re-ask", g.State(), robot.said, g.heard)
	}
}

func TestAnswerSleepsAfterSixtyStillSecondsAndTurnsHome(t *testing.T) {
	now := time.Now()
	robot, cam := &fakeRobot{}, &fakeCamera{}
	g := awake(Answer, robot, cam, now)
	g.Step(now.Add(59 * time.Second))
	if g.State() != Answer {
		t.Fatalf("slept after 59 s: state %s", g.State())
	}
	g.Step(now.Add(60 * time.Second))
	if g.State() != Sleep || robot.homes != 1 {
		t.Fatalf("state %s homes %d, want sleep and one turn home", g.State(), robot.homes)
	}
}

func TestSleepReturnsToIdleAndNeedsFreshMotion(t *testing.T) {
	now := time.Now()
	robot, cam := &fakeRobot{}, &fakeCamera{moving: true}
	g := awake(Sleep, robot, cam, now)
	g.motionSince = now.Add(-time.Hour)
	g.Step(now)
	if g.State() != Idle {
		t.Fatalf("state %s, want idle", g.State())
	}
	g.Step(now.Add(500 * time.Millisecond))
	if g.State() != Idle {
		t.Fatalf("woke on stale motion: state %s", g.State())
	}
}

func TestParseUtterance(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"I'm pulling port 5", 5},
		{"PORT 12 please", 12},
		{"I'm pulling port two.", 2},
		{"port forwarding", 0},
		{"the nas-01 box", 3},
		{"working on nas zero one", 0},
		{"nas 01", 3},
		{"moving the gs be18000", 2},
		{"deco ap", 1},
		{"Nass01", 3},
		{"just looking", 0},
		{"", 0},
	}
	for _, c := range cases {
		if got := ParseUtterance(c.text, table()); got != c.want {
			t.Errorf("%q: port %d, want %d", c.text, got, c.want)
		}
	}
}
