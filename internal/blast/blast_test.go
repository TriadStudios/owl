package blast

import (
	"os"
	"testing"
	"time"

	"github.com/triadstudios/owl/internal/discovery"
	"github.com/triadstudios/owl/internal/policy"
)

func TestAssessFixtureUplinkAndIdleHost(t *testing.T) {
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

	uplink, err := Assess(2, state, state, ids, rules)
	if err != nil {
		t.Fatal(err)
	}
	if uplink.Verdict != DoNotTouch || len(uplink.Criticality) == 0 || uplink.Criticality[0] != "uplink" {
		t.Errorf("port 2: verdict %s criticality %v, want do-not-touch from uplink; inputs %v", uplink.Verdict, uplink.Criticality, uplink.Inputs)
	}

	host, err := Assess(3, state, state, ids, rules)
	if err != nil {
		t.Fatal(err)
	}
	if host.Verdict != Safe || host.Liveness != Idle || host.Behind != 1 {
		t.Errorf("port 3: verdict %s liveness %s behind %d, want safe idle 1; inputs %v", host.Verdict, host.Liveness, host.Behind, host.Inputs)
	}
}
