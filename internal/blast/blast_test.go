package blast

import (
	"os"
	"testing"
	"time"

	"github.com/triadstudios/owl/internal/discovery"
	"github.com/triadstudios/owl/internal/perception"
	"github.com/triadstudios/owl/internal/policy"
)

func TestAssessFixtureVerdicts(t *testing.T) {
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

	rulesFile, err := os.Open("../../policy/rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer rulesFile.Close()
	rules, err := policy.Load(rulesFile)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name           string
		port           int
		attributionMin float64
		verdict        Verdict
		unknown        bool
	}{
		{"uplink neighbor", 2, 0.6, DoNotTouch, false},
		{"access point behind the port", 1, 0.6, DoNotTouch, false},
		{"idle single host", 3, 0.6, Safe, false},
		{"sfp cage the camera cannot see", 9, 0.6, "", true},
		{"attribution below minimum", 1, 0.7, "", true},
	}
	for _, c := range cases {
		card, err := Assess(c.port, Inputs{Prev: state, Cur: state, Camera: cam, AttributionMin: c.attributionMin, AggregationMin: 3, IDs: ids, Rules: rules})
		if err != nil {
			t.Fatal(err)
		}
		if card.Verdict != c.verdict || (card.Unknown != "") != c.unknown {
			t.Errorf("%s: port %d verdict %q unknown %q, want verdict %q unknown %t; inputs %v", c.name, c.port, card.Verdict, card.Unknown, c.verdict, c.unknown, card.Inputs)
		}
	}
}
