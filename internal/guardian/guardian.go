package guardian

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"
	"unicode"

	"github.com/triadstudios/owl/internal/blast"
	"github.com/triadstudios/owl/internal/reconcile"
)

type State string

const (
	Idle   State = "idle"
	Wake   State = "wake"
	Ask    State = "ask"
	Answer State = "answer"
	Sleep  State = "sleep"
)

const (
	wakeAfter  = time.Second
	sleepAfter = 60 * time.Second
	greeting   = "Hi, I'm watching this switch. What are you here for?"
	missLine   = "I didn't catch a port. Say port and a number, or the device's name."
)

type Point struct{ X, Y float64 }

type Frame struct {
	Centroid Point
}

type Robot interface {
	Turn(to Point)
	Say(text string)
	Listen() string
}

type Camera interface {
	Frame() Frame
	Motion() bool
}

type Guardian struct {
	Robot  Robot
	Camera Camera
	Table  func() []reconcile.Port
	Colors map[int]string
	Home   Point

	state       State
	motionSince time.Time
	lastMotion  time.Time
	heard       string
}

func (g *Guardian) State() State {
	if g.state == "" {
		return Idle
	}
	return g.state
}

func (g *Guardian) Step(now time.Time) {
	moving := g.Camera.Motion()
	if moving {
		g.lastMotion = now
	}
	switch g.State() {
	case Idle:
		if !moving {
			g.motionSince = time.Time{}
			return
		}
		if g.motionSince.IsZero() {
			g.motionSince = now
		}
		if now.Sub(g.motionSince) >= wakeAfter {
			g.Robot.Turn(g.Camera.Frame().Centroid)
			g.state = Wake
		}
	case Sleep:
		g.state, g.motionSince = Idle, time.Time{}
	default:
		if now.Sub(g.lastMotion) >= sleepAfter {
			g.Robot.Turn(g.Home)
			g.state = Sleep
			return
		}
		switch g.state {
		case Wake:
			g.Robot.Say(greeting)
			g.heard = g.Robot.Listen()
			g.state = Ask
		case Ask:
			ports := g.Table()
			num := ParseUtterance(g.heard, ports)
			if num == 0 {
				g.Robot.Say(missLine)
				g.heard = g.Robot.Listen()
				return
			}
			g.Robot.Say(Line(num, ports, g.Colors[num]))
			g.state = Answer
		}
	}
}

var portNumber = regexp.MustCompile(`(?i)\bport\s+(\d{1,3})\b`)

func ParseUtterance(text string, ports []reconcile.Port) int {
	if m := portNumber.FindStringSubmatch(text); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	words := strings.Fields(text)
	best, bestDist, tied := 0, 0, false
	for _, p := range ports {
		for _, name := range names(p) {
			want := squash(name)
			if len(want) < 3 {
				continue
			}
			for i := range words {
				for j := i + 1; j <= len(words) && j <= i+3; j++ {
					d := distance(squash(strings.Join(words[i:j], "")), want)
					if d > len(want)/4 {
						continue
					}
					switch {
					case best == 0 || d < bestDist:
						best, bestDist, tied = p.Num, d, false
					case d == bestDist && p.Num != best:
						tied = true
					}
				}
			}
		}
	}
	if tied {
		return 0
	}
	return best
}

func names(p reconcile.Port) []string {
	if p.Blast == nil {
		return nil
	}
	out := []string{p.Blast.Neighbor}
	for _, d := range p.Blast.Devices {
		out = append(out, d.Hostname)
	}
	return out
}

func squash(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, s)
}

func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

var lines = template.Must(template.New("").Parse(`
{{- define "do-not-touch"}}Port {{.Num}}, {{.Position}} from the left{{if .Color}}, {{.Color}} cable{{end}}. Do not touch: {{.Reason}}. {{.Behind}} {{if eq .Behind 1}}device{{else}}devices{{end}} behind it.{{end}}
{{- define "caution"}}Port {{.Num}}, {{.Position}} from the left{{if .Color}}, {{.Color}} cable{{end}}. {{.Downstream}}. Caution: {{.Liveness}}, {{.Behind}} {{if eq .Behind 1}}device{{else}}devices{{end}} behind it.{{end}}
{{- define "safe"}}Port {{.Num}}, {{.Position}} from the left{{if .Color}}, {{.Color}} cable{{end}}. {{.Downstream}}, {{.Liveness}}. Safe.{{end}}
{{- define "unknown"}}Port {{.Num}}: I can't tell whether it's safe, {{.Reason}}.{{end}}`))

type lineData struct {
	Num        int
	Position   string
	Color      string
	Reason     string
	Behind     int
	Downstream string
	Liveness   blast.Liveness
}

func Line(num int, ports []reconcile.Port, color string) string {
	d := lineData{Num: num, Color: color, Reason: fmt.Sprintf("port %d is not on the switch", num)}
	verdict := "unknown"
	position := 0
	for _, p := range ports {
		if p.Camera != "" && p.Num <= num {
			position++
		}
		if p.Num != num || p.Blast == nil {
			continue
		}
		c := p.Blast
		d.Behind, d.Liveness = c.Behind, c.Liveness
		d.Downstream, _, _ = strings.Cut(c.Downstream, ":")
		switch {
		case c.Unknown != "":
			d.Reason = c.Unknown
		case p.Camera == "":
			d.Reason = fmt.Sprintf("the camera cannot see port %d", num)
		default:
			verdict = string(c.Verdict)
			who := c.Neighbor
			if who == "" {
				who = d.Downstream
			}
			d.Reason = strings.Join(c.Criticality, " and ")
			if who != "" {
				d.Reason += ", " + who
			}
		}
	}
	d.Position = ordinal(position)
	var b strings.Builder
	if err := lines.ExecuteTemplate(&b, verdict, d); err != nil {
		return fmt.Sprintf("Port %d: I can't tell whether it's safe, %v.", num, err)
	}
	return b.String()
}

func ordinal(n int) string {
	suffix := "th"
	switch {
	case n%100 >= 11 && n%100 <= 13:
	case n%10 == 1:
		suffix = "st"
	case n%10 == 2:
		suffix = "nd"
	case n%10 == 3:
		suffix = "rd"
	}
	return strconv.Itoa(n) + suffix
}
