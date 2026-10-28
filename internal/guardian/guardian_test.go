package guardian

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/triadstudios/owl/internal/blast"
	"github.com/triadstudios/owl/internal/discovery"
	"github.com/triadstudios/owl/internal/narrate"
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
	return &Guardian{Robot: robot, Camera: cam, Table: table, Colors: map[int]string{2: "blue"}, state: state, lastMotion: now, lastSpeech: now}
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
	if g.State() != Ask || len(robot.said) != 1 || !slices.Contains(greetings, robot.said[0]) || g.heard != "I'm pulling port 3" {
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
		{"port 4", []string{"Port 4: unknown. I can't tell", "confidence 0.40"}},
		{"port 9", []string{"Port 9: unknown. I can't tell", "camera cannot see port 9"}},
		{"port 12", []string{"Port 12: unknown. I can't tell", "not on the switch"}},
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
	g.heard = "ok bye then"
	g.Step(now.Add(2 * time.Second))
	if g.State() != Sleep || robot.said[1] != closing || robot.homes != 1 {
		t.Fatalf("goodbye: state %s said %q homes %d, want a closing line and home", g.State(), robot.said, robot.homes)
	}
}

func TestAnswerListensForMoreThenCloses(t *testing.T) {
	now := time.Now()
	robot := &fakeRobot{}
	g := awake(Answer, robot, &fakeCamera{}, now)
	g.heard = "and port 3"
	g.Step(now.Add(time.Second))
	if g.State() != Answer || len(robot.said) != 1 || !strings.Contains(robot.said[0], "Port 3") || !strings.HasSuffix(robot.said[0], followUp) {
		t.Fatalf("follow-up port: state %s said %q, want port 3's card and a follow-up", g.State(), robot.said)
	}
	g.Step(now.Add(2 * time.Second))
	g.Step(now.Add(20 * time.Second))
	if g.State() != Answer || len(robot.said) != 1 {
		t.Fatalf("19 s of silence: state %s said %q, want it still listening", g.State(), robot.said)
	}
	g.Step(now.Add(21 * time.Second))
	if g.State() != Sleep || robot.said[1] != closing || robot.homes != 1 {
		t.Fatalf("20 s of silence: state %s said %q homes %d, want a closing line and home", g.State(), robot.said, robot.homes)
	}

	robot = &fakeRobot{}
	g = awake(Answer, robot, &fakeCamera{}, now)
	g.heard = "port 3"
	g.Step(now.Add(60 * time.Second))
	if g.State() != Sleep || robot.homes != 1 || len(robot.said) != 0 {
		t.Fatalf("60 s still: state %s homes %d said %q, want sleep and home without speaking", g.State(), robot.homes, robot.said)
	}
}

type fakeNarrator struct {
	reply narrate.Reply
	err   error
	delay time.Duration
}

func (n *fakeNarrator) Reply(ctx context.Context, req narrate.Request) (narrate.Reply, error) {
	select {
	case <-ctx.Done():
		return narrate.Reply{}, ctx.Err()
	case <-time.After(n.delay):
	}
	return n.reply, n.err
}

func TestNarratedLinesAreCheckedOrReplacedByTheTemplate(t *testing.T) {
	template := Line(2, table(), "blue") + " " + followUp
	cases := []struct {
		name  string
		heard string
		n     fakeNarrator
		want  string
	}{
		{"model line with the right port and verdict", "pulling port 2", fakeNarrator{reply: narrate.Reply{Port: 2, Line: "Port 2 is the uplink to GS-BE18000, do not touch it."}}, "Port 2 is the uplink to GS-BE18000, do not touch it."},
		{"model finds the port by device name", "the NAS please", fakeNarrator{reply: narrate.Reply{Port: 3, Line: "Port 3 is the NAS, it's safe."}}, "Port 3 is the NAS, it's safe."},
		{"model says safe for a do-not-touch port", "pulling port 2", fakeNarrator{reply: narrate.Reply{Port: 2, Line: "Port 2 is safe to pull."}}, template},
		{"model picks another port than the one said", "pulling port 2", fakeNarrator{reply: narrate.Reply{Port: 3, Line: "Port 3 is safe."}}, template},
		{"model slower than the budget", "pulling port 2", fakeNarrator{delay: 2 * time.Second, reply: narrate.Reply{Port: 2, Line: "Port 2, do not touch."}}, template},
		{"model error", "pulling port 2", fakeNarrator{err: errors.New("503")}, template},
		{"model chat with a verdict and no port", "hello", fakeNarrator{reply: narrate.Reply{Line: "Hi! Everything here is safe."}}, missLine},
		{"model chat", "hello", fakeNarrator{reply: narrate.Reply{Unclear: true, Line: "Hi! Which cable are you after?"}}, "Hi! Which cable are you after?"},
	}
	for _, c := range cases {
		now := time.Now()
		robot := &fakeRobot{}
		g := awake(Ask, robot, &fakeCamera{}, now)
		g.Narrator = &c.n
		g.heard = c.heard
		g.Step(now.Add(time.Second))
		if len(robot.said) != 1 || robot.said[0] != c.want {
			t.Errorf("%s: said %q, want %q", c.name, robot.said, c.want)
		}
	}
}

func TestBackgroundTalkEndsAfterThreeUnclearTurns(t *testing.T) {
	now := time.Now()
	robot := &fakeRobot{replies: []string{"in the dark", "oh oh oh oh"}}
	g := awake(Ask, robot, &fakeCamera{moving: true}, now)
	g.heard = "from the last and the strength"
	for i := 1; i <= 3; i++ {
		g.Step(now.Add(time.Duration(i) * time.Second))
	}
	if g.State() != Sleep || robot.homes != 1 || robot.said[2] != closing {
		t.Fatalf("state %s homes %d said %q, want a closing line and home after three unclear turns", g.State(), robot.homes, robot.said)
	}
}

func TestSleepWaitsForAQuietRoomBeforeIdle(t *testing.T) {
	now := time.Now()
	robot, cam := &fakeRobot{}, &fakeCamera{moving: true}
	g := awake(Sleep, robot, cam, now)
	g.Step(now.Add(5 * time.Second))
	if g.State() != Sleep {
		t.Fatalf("idle while someone is still there: state %s", g.State())
	}
	cam.moving = false
	g.Step(now.Add(14 * time.Second))
	if g.State() != Sleep {
		t.Fatalf("idle after 9 quiet seconds: state %s", g.State())
	}
	g.Step(now.Add(15 * time.Second))
	if g.State() != Idle {
		t.Fatalf("state %s after 10 quiet seconds, want idle", g.State())
	}
	g.Step(now.Add(16 * time.Second))
	if g.State() != Idle {
		t.Fatalf("woke without fresh motion: state %s", g.State())
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
		{"Hello. Need a pool port too.", 2},
		{"the full part 2.", 2},
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
