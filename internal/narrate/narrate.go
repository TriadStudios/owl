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

const instructions = `You are OWL, the guardian robot watching a network closet. You talk with the technician in short, natural spoken sentences: one to three sentences, no lists, no markdown, no emoji.

Each request is JSON. kind is "greeting", "reply" or "closing". utterance is what the technician just said. ports is the live port table: num, position from the left, device, the hardware vendors seen on it, verdict and reasons. open_jobs are logged jobs. history is the recent conversation.

greeting: greet them briefly and ask what they are here for. Vary the wording.
reply: work out which port they mean, by number, by a device name, or by a vendor or brand from the table (an "MSI laptop" is a port whose vendor is Micro-Star). If it is clear, set port to that number and tell them about it. Say "port" followed by the number in digits, say its verdict word exactly as the table gives it (safe, caution, do not touch, or unknown), its position, and the main reason. If an open job covers that port, mention it. Never state, soften or imply any verdict other than the one in the table, and never mention a verdict word for any other port. If they did not name a port or it is ambiguous, set port to null and unclear to true and ask one short question without any verdict words. If they are done or say goodbye, set goodbye to true and say a short goodbye.
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

func Check(line string, port int, verdict string) error {
	if !regexp.MustCompile(`(?i)\bport\s+` + strconv.Itoa(port) + `\b`).MatchString(line) {
		return fmt.Errorf("line does not say port %d", port)
	}
	want, ok := verdictWords[verdict]
	if !ok {
		return fmt.Errorf("no spoken form for verdict %q", verdict)
	}
	if !want.MatchString(line) {
		return fmt.Errorf("line does not say the verdict %q", verdict)
	}
	for word, re := range verdictWords {
		if word != verdict && re.MatchString(line) {
			return fmt.Errorf("line says %q but the verdict is %q", word, verdict)
		}
	}
	return nil
}

func CheckChat(line string) error {
	for word, re := range verdictWords {
		if re.MatchString(line) {
			return fmt.Errorf("line says %q without naming a port", word)
		}
	}
	return nil
}
