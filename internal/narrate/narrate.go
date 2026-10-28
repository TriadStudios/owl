package narrate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

const (
	DefaultURL   = "https://api.meta.ai/v1/responses"
	DefaultModel = "muse-spark-1.3"
	maxReply     = 64 << 10
	maxLine      = 400
)

const instructions = `You are OWL, a small robot that watches a network closet and keeps technicians from pulling the wrong cable. Talk like a helpful colleague standing next to them: short, natural spoken sentences, usually one or two, no lists, no markdown, no emoji. Answer what they actually said and what they are about to do. Don't follow a fixed formula and don't repeat details you already gave.

Each request is JSON. kind is "greeting", "reply" or "closing". utterance is what the technician just said, from speech to text, so it may be misheard or cut off. ports is the live port table: num, position from the left, device, vendors, verdict and reasons. open_jobs are logged jobs. history is this conversation so far. scene, when present, is what the camera sees right now: hand_port is the port their hand is at or reaching for, and warnings_spoken are warnings you already called out during this visit.

greeting: greet them briefly and ask what they are here to do. Vary the wording.

reply: set port to the port the conversation is about now. Work it out from the utterance, from history ("it", "that one" or "the cable" usually mean the port you just talked about), from a device or vendor name in the table (an "MSI laptop" is a port whose vendor is Micro-Star), or from scene.hand_port when they say "this one". Only when none of that settles it, set port to null and unclear to true and ask one short question.
The first time you talk about a port, say "port" and its number in digits, then its verdict word exactly as the table gives it (safe, caution, do not touch or unknown) and what matters in a few words: what is on it and what would break. After that, talk naturally; you don't have to repeat the verdict.
If scene.hand_port is set and is not the port they want, tell them straight away that their hand is on the wrong port and which way to move (left or right, how many over).
If they say they are about to unplug something, respond to that: for caution say what will drop off, for do not touch tell them not to, for safe tell them to go ahead.
If a warning in warnings_spoken is about what they are doing, acknowledge it briefly.
Never state, soften or imply a verdict other than the table's. Every verdict word you say must come after the port number it belongs to, and before any other port number.
If they are done or say goodbye, set goodbye to true and say a short goodbye.

closing: they have gone quiet; say a short goodbye.

Return only JSON: {"port": number or null, "unclear": boolean, "goodbye": boolean, "line": string}`

type Port struct {
	Num      int      `json:"num"`
	Position string   `json:"position"`
	Device   string   `json:"device,omitempty"`
	Vendors  []string `json:"vendors,omitempty"`
	Verdict  string   `json:"verdict"`
	Reasons  []string `json:"reasons,omitempty"`
}

type Job struct {
	ID    int    `json:"id"`
	Port  int    `json:"port"`
	Tech  string `json:"tech"`
	Note  string `json:"note,omitempty"`
	Until string `json:"until"`
}

type Turn struct {
	Who  string `json:"who"`
	Text string `json:"text"`
}

type Request struct {
	Kind      string `json:"kind"`
	Utterance string `json:"utterance,omitempty"`
	Ports     []Port `json:"ports"`
	Jobs      []Job  `json:"open_jobs"`
	History   []Turn `json:"history,omitempty"`
	Scene     *Scene `json:"scene,omitempty"`
}

type Scene struct {
	HandPort int      `json:"hand_port,omitempty"`
	Warnings []string `json:"warnings_spoken,omitempty"`
}

type Reply struct {
	Port    int
	Unclear bool
	Goodbye bool
	Line    string
}

type Client struct {
	URL   string
	Model string
	Key   string
	HTTP  *http.Client
}

func (c *Client) Reply(ctx context.Context, req Request) (Reply, error) {
	prompt, err := json.Marshal(req)
	if err != nil {
		return Reply{}, err
	}
	body, err := json.Marshal(map[string]any{
		"model":        c.Model,
		"store":        false,
		"reasoning":    map[string]string{"effort": "minimal"},
		"instructions": instructions,
		"input":        []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]string{"type": "input_text", "text": string(prompt)}}}},
	})
	if err != nil {
		return Reply{}, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return Reply{}, err
	}
	hreq.Header.Set("Authorization", "Bearer "+c.Key)
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(hreq)
	if err != nil {
		return Reply{}, fmt.Errorf("narrate: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxReply))
	if err != nil {
		return Reply{}, fmt.Errorf("narrate: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Reply{}, fmt.Errorf("narrate: %s: %.300s", resp.Status, bytes.TrimSpace(data))
	}
	var out struct {
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return Reply{}, fmt.Errorf("narrate: decode: %w", err)
	}
	if out.Status != "completed" {
		return Reply{}, fmt.Errorf("narrate: response %q", out.Status)
	}
	var text strings.Builder
	for _, o := range out.Output {
		for _, c := range o.Content {
			if o.Type == "message" && c.Type == "output_text" {
				text.WriteString(c.Text)
			}
		}
	}
	return parse(text.String())
}

func (c *Client) Warm(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(c.URL, "/responses")+"/models", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("narrate warm: %w", err)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, maxReply)); err != nil {
		return fmt.Errorf("narrate warm: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("narrate warm: %s", resp.Status)
	}
	return nil
}

func parse(text string) (Reply, error) {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(strings.TrimPrefix(text, "```json"), "```")
	text = strings.TrimSpace(strings.TrimSuffix(text, "```"))
	var r struct {
		Port    *int   `json:"port"`
		Unclear bool   `json:"unclear"`
		Goodbye bool   `json:"goodbye"`
		Line    string `json:"line"`
	}
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		return Reply{}, fmt.Errorf("narrate: reply is not the JSON asked for: %.120s", text)
	}
	line := strings.TrimSpace(r.Line)
	if line == "" || len(line) > maxLine {
		return Reply{}, fmt.Errorf("narrate: line of %d bytes", len(line))
	}
	reply := Reply{Unclear: r.Unclear, Goodbye: r.Goodbye, Line: line}
	if r.Port != nil {
		if *r.Port < 1 {
			return Reply{}, fmt.Errorf("narrate: port %d", *r.Port)
		}
		reply.Port = *r.Port
	}
	return reply, nil
}

var verdictWords = map[string]*regexp.Regexp{
	"safe":         regexp.MustCompile(`(?i)\bsafe\b`),
	"caution":      regexp.MustCompile(`(?i)\bcaution\b`),
	"do not touch": regexp.MustCompile(`(?i)\bdo[\s-]+not[\s-]+touch\b`),
	"unknown":      regexp.MustCompile(`(?i)\bunknown\b`),
}

func SpokenVerdict(verdict string) string {
	if verdict == "do-not-touch" {
		return "do not touch"
	}
	return verdict
}

var portMention = regexp.MustCompile(`(?i)\bport\s+(\d+)\b`)

func Check(line string, focus int, verdicts map[int]string, mustSay bool) error {
	marks := portMention.FindAllStringSubmatchIndex(line, -1)
	said := false
	for i := -1; i < len(marks); i++ {
		port, from, to := focus, 0, len(line)
		if i >= 0 {
			port, _ = strconv.Atoi(line[marks[i][2]:marks[i][3]])
			from = marks[i][1]
		}
		if i+1 < len(marks) {
			to = marks[i+1][0]
		}
		want := ""
		if port != 0 {
			want = verdicts[port]
		}
		for word, re := range verdictWords {
			if !re.MatchString(line[from:to]) {
				continue
			}
			if word != want {
				return fmt.Errorf("line says %q about port %d, whose verdict is %q", word, port, want)
			}
			if i >= 0 && port == focus {
				said = true
			}
		}
	}
	if mustSay && !said {
		return fmt.Errorf("line does not say port %d and its verdict %q", focus, verdicts[focus])
	}
	return nil
}
