package guardian

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/triadstudios/owl/internal/narrate"
	"github.com/triadstudios/owl/internal/perception"
	"github.com/triadstudios/owl/internal/reconcile"
)

type Reach struct {
	Track func(ctx context.Context) (<-chan perception.TrackFrame, error)
	Alert func(text string) error
	Clip  func() (string, error)
	Log   *slog.Logger

	mu     sync.Mutex
	hand   int
	warned []string
}

const maxWarnings = 3

func (r *Reach) Scene() narrate.Scene {
	r.mu.Lock()
	defer r.mu.Unlock()
	return narrate.Scene{HandPort: r.hand, Warnings: slices.Clone(r.warned)}
}

func (r *Reach) see(port int) {
	r.mu.Lock()
	r.hand = port
	r.mu.Unlock()
}

func (r *Reach) reset() {
	r.mu.Lock()
	r.hand, r.warned = 0, nil
	r.mu.Unlock()
}

func StopLine(port int, ports []reconcile.Port) string {
	_, d := facts(port, ports, "")
	return fmt.Sprintf("Stop! Port %d is do not touch: %s.", port, d.Reason)
}

func DeviationLine(port, jobPort int, doNotTouch bool) string {
	if doNotTouch {
		return fmt.Sprintf("Stop! Port %d isn't your job, and it's do not touch. Your job is port %d.", port, jobPort)
	}
	return fmt.Sprintf("Careful, port %d isn't your job. Your job is port %d.", port, jobPort)
}

func WarningLines(a *Approach, ports []reconcile.Port) []string {
	var out []string
	for _, p := range a.Ports {
		if _, job := a.JobPorts[p.Num]; job {
			continue
		}
		jobPort := 0
		for port := range a.JobPorts {
			if jobPort == 0 || port < jobPort {
				jobPort = port
			}
		}
		switch {
		case jobPort != 0 && (a.Populated[p.Num] || a.DoNotTouch[p.Num]):
			out = append(out, DeviationLine(p.Num, jobPort, a.DoNotTouch[p.Num]))
		case jobPort == 0 && a.DoNotTouch[p.Num]:
			out = append(out, StopLine(p.Num, ports))
		}
	}
	return out
}

func (r *Reach) Run(ctx context.Context, a *Approach, ports []reconcile.Port) {
	frames, err := r.Track(ctx)
	if err != nil {
		r.Log.Error("track", "err", err)
		return
	}
	defer r.Log.Info("reach", "event", "tracking stopped")
	r.reset()
	defer r.reset()
	var seenAt, lockedAt time.Time
	frozen := false
	for f := range frames {
		if f.Err != "" {
			r.Log.Error("track stream ended", "err", f.Err)
			return
		}
		if f.Frozen != frozen {
			frozen = f.Frozen
			r.Log.Info("attribution", "frozen", frozen, "at", f.T)
		}
		var obs *HandObs
		if f.Hand != nil {
			obs = &HandObs{T: f.T, Box: *f.Hand}
		}
		for _, e := range a.Update(obs, f.T) {
			switch e.Kind {
			case HandSeen:
				seenAt = f.T
				r.Log.Info("reach", "event", e.Kind, "at", f.T, "source", f.Source)
			case TargetLocked:
				lockedAt = f.T
				r.see(e.Port)
				r.Log.Info("reach", "event", e.Kind, "at", f.T, "port", e.Port, "confidence", round2(e.Confidence), "distance_px", int(e.Distance))
			case Expected:
				r.Log.Info("reach", "event", e.Kind, "at", f.T, "port", e.Port, "job", e.JobID)
			case Withdrawn:
				r.see(0)
				r.Log.Info("reach", "event", e.Kind, "at", f.T)
			case Stop, Deviation:
				text := StopLine(e.Port, ports)
				if e.Kind == Deviation {
					text = DeviationLine(e.Port, e.JobPort, a.DoNotTouch[e.Port])
				}
				r.Log.Info("reach", "event", e.Kind, "at", f.T, "port", e.Port, "job", e.JobID, "confidence", round2(e.Confidence), "distance_px", int(e.Distance), "text", text)
				go r.warn(e, text, f.T, seenAt, lockedAt)
			}
		}
	}
}

func (r *Reach) warn(e ApproachEvent, text string, decided, seenAt, lockedAt time.Time) {
	start := time.Now()
	if err := r.Alert(text); err != nil {
		r.Log.Error("warning", "port", e.Port, "err", err)
		return
	}
	r.mu.Lock()
	r.warned = append(r.warned, text)
	if len(r.warned) > maxWarnings {
		r.warned = r.warned[1:]
	}
	r.mu.Unlock()
	r.Log.Info("reach", "event", "warning spoken", "at", time.Now(), "port", e.Port, "kind", e.Kind,
		"alert_ms", time.Since(start).Milliseconds(), "since_lock_ms", start.Sub(lockedAt).Milliseconds(), "since_seen_ms", start.Sub(seenAt).Milliseconds(), "decided_at", decided)
	hash, err := r.Clip()
	if err != nil {
		r.Log.Error("clip", "port", e.Port, "err", err)
		return
	}
	r.Log.Info("reach", "event", "clip saved", "port", e.Port, "kind", e.Kind, "evidence", hash)
}

func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}
