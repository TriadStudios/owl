package guardian

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/triadstudios/owl/internal/narrate"
	"github.com/triadstudios/owl/internal/reconcile"
)

const (
	maxHistory = 8
	maxVendors = 5
)

var goodbye = regexp.MustCompile(`(?i)\b(bye|goodbye|that's all|that is all|that's it|nothing else|thanks|thank you|i'm done|all done)\b`)

func (g *Guardian) log() *slog.Logger {
	if g.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return g.Log
}

func (g *Guardian) say(line string) {
	g.Robot.Say(line)
	g.remember("robot", line)
}

func (g *Guardian) remember(who, text string) {
	g.history = append(g.history, narrate.Turn{Who: who, Text: text})
	if len(g.history) > maxHistory {
		g.history = g.history[len(g.history)-maxHistory:]
	}
}

func (g *Guardian) ports() []reconcile.Port {
	if g.visit != nil {
		return g.visit
	}
	return g.Table()
}

func (g *Guardian) verdicts(ports []reconcile.Port) map[int]string {
	out := map[int]string{}
	for _, p := range ports {
		verdict, _ := facts(p.Num, ports, g.Colors[p.Num])
		out[p.Num] = narrate.SpokenVerdict(verdict)
	}
	return out
}

func (g *Guardian) ask(kind, utterance string, ports []reconcile.Port) (narrate.Reply, time.Duration, error) {
	if g.Narrator == nil {
		return narrate.Reply{}, 0, fmt.Errorf("no narrator")
	}
	var jobs []narrate.Job
	if g.Jobs != nil {
		jobs = g.Jobs()
	}
	ctx, cancel := context.WithTimeout(context.Background(), narrateBudget)
	defer cancel()
	start := time.Now()
	req := narrate.Request{Kind: kind, Utterance: utterance, Ports: g.narratePorts(ports), Jobs: jobs, History: g.history}
	if g.Scene != nil {
		if s := g.Scene(); s.HandPort != 0 || len(s.Warnings) > 0 {
			req.Scene = &s
		}
	}
	r, err := g.Narrator.Reply(ctx, req)
	return r, time.Since(start), err
}

func (g *Guardian) chat(kind, utterance, fallback string) string {
	ports := g.ports()
	r, took, err := g.ask(kind, utterance, ports)
	if err == nil {
		err = narrate.Check(r.Line, 0, g.verdicts(ports), false)
	}
	if err != nil {
		g.log().Info("narrate fallback", "kind", kind, "reason", err.Error(), "took_ms", took.Milliseconds())
		return fallback
	}
	return r.Line
}

func (g *Guardian) respond(heard string) (string, int, bool) {
	g.remember("tech", heard)
	ports := g.ports()
	parsed := ParseUtterance(heard, ports)
	bye := goodbye.MatchString(heard)
	r, took, err := g.ask("reply", heard, ports)
	switch {
	case err != nil:
	case parsed != 0 && r.Port != parsed:
		err = fmt.Errorf("model chose port %d, the utterance says port %d", r.Port, parsed)
	case r.Port != 0:
		verdict, _ := facts(r.Port, ports, g.Colors[r.Port])
		if err = narrate.Check(r.Line, r.Port, g.verdicts(ports), !g.told[r.Port]); err == nil {
			g.tell(r.Port)
			g.logSafety(heard, r.Port, verdict, "model", r.Line, "", took, ports)
			return r.Line, r.Port, r.Goodbye
		}
	default:
		if err = narrate.Check(r.Line, 0, g.verdicts(ports), false); err == nil {
			return r.Line, 0, r.Goodbye
		}
	}
	switch {
	case parsed != 0:
		verdict, _ := facts(parsed, ports, g.Colors[parsed])
		line := Line(parsed, ports, g.Colors[parsed]) + " " + followUp
		g.tell(parsed)
		g.logSafety(heard, parsed, verdict, "template", line, errText(err), took, ports)
		return line, parsed, false
	case bye:
		g.log().Info("narrate fallback", "kind", "reply", "reason", errText(err), "took_ms", took.Milliseconds())
		return closing, 0, true
	default:
		g.log().Info("narrate fallback", "kind", "reply", "reason", errText(err), "took_ms", took.Milliseconds())
		return missLine, 0, false
	}
}

func (g *Guardian) tell(port int) {
	if g.told == nil {
		g.told = map[int]bool{}
	}
	g.told[port] = true
}

func (g *Guardian) logSafety(heard string, port int, verdict, source, line, fallback string, took time.Duration, ports []reconcile.Port) {
	var inputs []string
	for _, p := range ports {
		if p.Num == port && p.Blast != nil {
			inputs = p.Blast.Inputs
		}
	}
	g.log().Info("safety line", "port", port, "verdict", verdict, "source", source, "text", line, "utterance", heard, "fallback_reason", fallback, "narrate_ms", took.Milliseconds(), "inputs", inputs)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (g *Guardian) narratePorts(ports []reconcile.Port) []narrate.Port {
	var out []narrate.Port
	for _, p := range ports {
		verdict, d := facts(p.Num, ports, g.Colors[p.Num])
		np := narrate.Port{Num: p.Num, Position: d.Position + " from the left", Device: d.Downstream, Verdict: narrate.SpokenVerdict(verdict)}
		if p.Camera == "" {
			np.Position = "not visible to the camera"
		}
		if d.Reason != "" {
			np.Reasons = append(np.Reasons, d.Reason)
		}
		if p.Blast != nil {
			np.Reasons = append(np.Reasons, fmt.Sprintf("%d %s behind it", d.Behind, plural(d.Behind, "device")), fmt.Sprintf("traffic %s", d.Liveness))
			for _, dev := range p.Blast.Devices {
				if dev.Vendor != "" && !slices.Contains(np.Vendors, dev.Vendor) && len(np.Vendors) < maxVendors {
					np.Vendors = append(np.Vendors, dev.Vendor)
				}
			}
		}
		if d.Color != "" {
			np.Reasons = append(np.Reasons, d.Color+" cable")
		}
		out = append(out, np)
	}
	return out
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

const maxDeviceTerms = 25

func DeviceTerms(ports []reconcile.Port) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range ports {
		for _, name := range names(p) {
			term := strings.Join(strings.FieldsFunc(name, func(r rune) bool {
				return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
			}), " ")
			if len(term) < 3 || len(term) > 40 || seen[strings.ToLower(term)] {
				continue
			}
			seen[strings.ToLower(term)] = true
			out = append(out, term)
			if len(out) == maxDeviceTerms {
				return out
			}
		}
	}
	return out
}
