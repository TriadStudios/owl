package drift

import (
	"os"
	"testing"
	"time"

	"github.com/triadstudios/owl/internal/perception"
)

func load(t *testing.T, path string) *perception.Result {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, err := perception.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFixtureWithOneCableMovedAndOneRemoved(t *testing.T) {
	base := load(t, "../../testdata/perception-switch-face.json")
	edited := load(t, "../../testdata/perception-switch-face-drift.json")
	now := time.Now()

	if same := Compare(base, base, now); len(same) != 0 {
		t.Fatalf("baseline against itself: %v", same)
	}

	want := map[int]string{
		1: "occupied -> empty",
		3: "occupied -> empty",
		5: "empty -> occupied",
	}
	got := Compare(base, edited, now)
	if len(got) != len(want) {
		t.Fatalf("findings %v, want ports 1, 3 and 5", got)
	}
	for _, f := range got {
		if f.Type != "drift" || want[f.Port] != f.Diff || f.BeforeFrame != base.FrameHash || f.AfterFrame != edited.FrameHash {
			t.Errorf("port %d: %+v, want drift %q between %s and %s", f.Port, f, want[f.Port], base.FrameHash[:8], edited.FrameHash[:8])
		}
	}

	rerouted := load(t, "../../testdata/perception-switch-face-drift.json")
	rerouted.Ports[1].CablePolygon = base.Ports[0].CablePolygon
	if got := Compare(edited, rerouted, now); len(got) != 1 || got[0].Port != 2 {
		t.Errorf("port 2 cable swapped for port 1's outline: findings %v, want one on port 2", got)
	}
}
