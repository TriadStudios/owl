package guardian

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/triadstudios/owl/internal/finding"
	"github.com/triadstudios/owl/internal/job"
)

func TestALinkChangeIsConfirmedAndLogged(t *testing.T) {
	polls := []map[int]bool{
		{2: true, 3: true},
		{2: true, 3: true},
		{2: true, 3: false},
		{2: false, 3: false},
		{2: false, 3: true},
	}
	var said []string
	var found []finding.Finding
	ctx, cancel := context.WithCancel(context.Background())
	p := &Pulls{
		Links: func() (map[int]bool, error) {
			if len(polls) == 1 {
				cancel()
			}
			links := polls[0]
			if len(polls) > 1 {
				polls = polls[1:]
			}
			return links, nil
		},
		Jobs:   func() []job.Job { return nil },
		Alert:  func(text string) error { said = append(said, text); return nil },
		Clip:   func() (string, error) { return "clip", nil },
		Record: func(f finding.Finding) error { found = append(found, f); return nil },
		Every:  time.Millisecond,
		Log:    slog.New(slog.DiscardHandler),
	}
	p.Run(ctx, table())
	kinds := []string{}
	for _, f := range found {
		kinds = append(kinds, f.Type)
		if f.Evidence != "clip" {
			t.Errorf("finding %+v has no clip", f)
		}
	}
	if !slices.Equal(kinds, []string{"pull", "wrong-pull", "replug"}) || len(said) != 3 {
		t.Fatalf("findings %v said %q, want pull, wrong-pull, replug", kinds, said)
	}
	if said[0] != "Confirmed on the switch, port 3 is down." || !strings.Contains(said[1], "do not touch: that's GS-BE18000, the uplink") || said[2] != "Port 3 is back up on the switch." {
		t.Errorf("said %q", said)
	}
}

func TestAPullOffAnOpenJobIsWrong(t *testing.T) {
	now := time.Now()
	jobs := []job.Job{{ID: 7, Port: 3, Start: now.Add(-time.Hour), End: now.Add(time.Hour)}}
	for _, c := range []struct {
		port int
		kind string
		job  int
	}{{3, "pull", 7}, {9, "wrong-pull", 0}} {
		var found []finding.Finding
		p := &Pulls{
			Jobs:   func() []job.Job { return jobs },
			Alert:  func(string) error { return nil },
			Clip:   func() (string, error) { return "", nil },
			Record: func(f finding.Finding) error { found = append(found, f); return nil },
			Log:    slog.New(slog.DiscardHandler),
		}
		p.changed(c.port, false, table(), now)
		if len(found) != 1 || found[0].Type != c.kind || found[0].JobID != c.job {
			t.Errorf("port %d: %+v, want %s on job %d", c.port, found, c.kind, c.job)
		}
	}
}
