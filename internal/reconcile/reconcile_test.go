package reconcile

import (
	"os"
	"testing"
	"time"

	"github.com/triadstudios/owl/internal/discovery"
	"github.com/triadstudios/owl/internal/perception"
)

func TestJoinFixtureFrameAllCopperConsistent(t *testing.T) {
	camFile, err := os.Open("../../testdata/perception-switch-face.json")
	if err != nil {
		t.Fatal(err)
	}
	defer camFile.Close()
	cam, err := perception.Decode(camFile)
	if err != nil {
		t.Fatal(err)
	}

	walkFile, err := os.Open("../../testdata/walk-datto.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer walkFile.Close()
	vars, err := discovery.ParseWalk(walkFile)
	if err != nil {
		t.Fatal(err)
	}
	state, err := discovery.Build(vars, time.Time{})
	if err != nil {
		t.Fatal(err)
	}

	leaseFile, err := os.Open("../../testdata/leases.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer leaseFile.Close()
	ids, err := discovery.LoadIdentities(leaseFile)
	if err != nil {
		t.Fatal(err)
	}

	byNum := map[int]Port{}
	for _, p := range Join(cam, state, ids) {
		byNum[p.Num] = p
	}
	for n := 1; n <= 8; n++ {
		p, ok := byNum[n]
		if !ok || p.Status != Consistent {
			t.Errorf("port %d: %+v, want consistent", n, p)
		}
	}
}
