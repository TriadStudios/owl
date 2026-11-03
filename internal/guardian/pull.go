package guardian

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/triadstudios/owl/internal/finding"
	"github.com/triadstudios/owl/internal/job"
	"github.com/triadstudios/owl/internal/reconcile"
)

type Pulls struct {
	Links  func() (map[int]bool, error)
	Jobs   func() []job.Job
	Alert  func(text string) error
	Clip   func() (string, error)
	Record func(finding.Finding) error
	Every  time.Duration
	Log    *slog.Logger
}

func (p *Pulls) Run(ctx context.Context, ports []reconcile.Port) {
	var last map[int]bool
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(p.Every):
		}
		links, err := p.Links()
		if err != nil {
			p.Log.Warn("link poll", "err", err)
			continue
		}
		for num, up := range links {
			if was, seen := last[num]; seen && was != up {
				p.changed(num, up, ports, time.Now())
			}
		}
		last = links
	}
}

func (p *Pulls) changed(num int, up bool, ports []reconcile.Port, now time.Time) {
	f := finding.Finding{Type: "pull", Port: num, TS: now.UTC()}
	text := fmt.Sprintf("Confirmed on the switch, port %d is down.", num)
	jobs := p.Jobs()
	if j := job.Covering(jobs, num, now); j != nil {
		f.JobID = j.ID
	}
	open := false
	for _, j := range jobs {
		open = open || j.Closed.IsZero() && !now.Before(j.Start) && !now.After(j.End)
	}
	verdict, d := facts(num, ports, "")
	switch {
	case up:
		f.Type, text = "replug", fmt.Sprintf("Port %d is back up on the switch.", num)
	case verdict == "do-not-touch" && f.JobID == 0:
		f.Type, text = "wrong-pull", fmt.Sprintf("Port %d just went down on the switch, and it's do not touch: %s.", num, d.Reason)
	case open && f.JobID == 0:
		f.Type, text = "wrong-pull", fmt.Sprintf("Port %d just went down on the switch, and it isn't part of the open job.", num)
	}
	p.Log.Info("link change", "port", num, "up", up, "kind", f.Type, "verdict", verdict, "job", f.JobID, "text", text)
	if err := p.Alert(text); err != nil {
		p.Log.Error("link change alert", "port", num, "err", err)
	}
	hash, err := p.Clip()
	if err != nil {
		p.Log.Warn("link change clip", "port", num, "err", err)
	}
	f.Evidence, f.Text = hash, text
	if err := p.Record(f); err != nil {
		p.Log.Error("link change record", "port", num, "err", err)
	}
}
