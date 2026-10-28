package perception

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

const maxResponseBytes = 4 << 20

type Result struct {
	Ports     []Port     `json:"ports"`
	Row       [2]float64 `json:"row"`
	LEDs      []LED      `json:"leds"`
	FrameHash string     `json:"frame_hash"`
}

type LED struct {
	Num            int     `json:"num"`
	LinkOn         bool    `json:"link_on"`
	ActivityEnergy float64 `json:"activity_energy"`
}

type Port struct {
	Num          int          `json:"num"`
	Occupied     bool         `json:"occupied"`
	Box          [4]float64   `json:"box"`
	Color        string       `json:"color"`
	CablePolygon [][2]float64 `json:"cable_polygon,omitempty"`
	Confidence   *float64     `json:"confidence"`
}

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

var frameHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (c *Client) Segment(ctx context.Context, image []byte, contentType string) (*Result, error) {
	body, err := c.post(ctx, "/segment", image, contentType)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return Decode(io.LimitReader(body, maxResponseBytes))
}

func (c *Client) SegmentCamera(ctx context.Context, burst time.Duration) (*Result, error) {
	body, err := c.post(ctx, fmt.Sprintf("/segment?burst=%g", burst.Seconds()), nil, "")
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return Decode(io.LimitReader(body, maxResponseBytes))
}

func (c *Client) Capture(ctx context.Context, frames int, spacing time.Duration) ([]string, error) {
	body, err := c.post(ctx, fmt.Sprintf("/capture?frames=%d&spacing=%g", frames, spacing.Seconds()), nil, "")
	if err != nil {
		return nil, err
	}
	defer body.Close()
	var res struct {
		Frames []string `json:"frames"`
	}
	if err := json.NewDecoder(io.LimitReader(body, maxResponseBytes)).Decode(&res); err != nil {
		return nil, fmt.Errorf("perception: decode capture: %w", err)
	}
	if len(res.Frames) != frames {
		return nil, fmt.Errorf("perception: %d frames captured, asked for %d", len(res.Frames), frames)
	}
	for _, h := range res.Frames {
		if !frameHash.MatchString(h) {
			return nil, fmt.Errorf("perception: bad frame hash %q", h)
		}
	}
	return res.Frames, nil
}

func (c *Client) LEDs(ctx context.Context, boxes [][4]float64, window time.Duration) ([]LED, error) {
	req, err := json.Marshal(map[string]any{"boxes": boxes, "seconds": window.Seconds()})
	if err != nil {
		return nil, err
	}
	body, err := c.post(ctx, "/leds", req, "application/json")
	if err != nil {
		return nil, err
	}
	defer body.Close()
	var res struct {
		LEDs []LED `json:"leds"`
	}
	if err := json.NewDecoder(io.LimitReader(body, maxResponseBytes)).Decode(&res); err != nil {
		return nil, fmt.Errorf("perception: decode leds: %w", err)
	}
	if len(res.LEDs) != len(boxes) {
		return nil, fmt.Errorf("perception: %d LED readings for %d boxes", len(res.LEDs), len(boxes))
	}
	for i, l := range res.LEDs {
		if l.Num != i+1 || l.ActivityEnergy < 0 {
			return nil, fmt.Errorf("perception: bad LED entry at position %d", i+1)
		}
	}
	return res.LEDs, nil
}

func (c *Client) MotionStart(ctx context.Context, regions [][4]int) error {
	req, err := json.Marshal(map[string]any{"regions": regions})
	if err != nil {
		return err
	}
	body, err := c.post(ctx, "/motion/start", req, "application/json")
	if err != nil {
		return err
	}
	return body.Close()
}

func (c *Client) MotionStop(ctx context.Context, regions int) ([]float64, error) {
	body, err := c.post(ctx, "/motion/stop", nil, "")
	if err != nil {
		return nil, err
	}
	defer body.Close()
	var res struct {
		Changes []float64 `json:"changes"`
	}
	if err := json.NewDecoder(io.LimitReader(body, maxResponseBytes)).Decode(&res); err != nil {
		return nil, fmt.Errorf("perception: decode motion: %w", err)
	}
	if len(res.Changes) != regions {
		return nil, fmt.Errorf("perception: %d motion values for %d regions", len(res.Changes), regions)
	}
	return res.Changes, nil
}

func (c *Client) post(ctx context.Context, path string, payload []byte, contentType string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("perception: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		msg, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		if err != nil {
			return nil, fmt.Errorf("perception: %s (reading body: %w)", resp.Status, err)
		}
		return nil, fmt.Errorf("perception: %s: %s", resp.Status, bytes.TrimSpace(msg))
	}
	return resp.Body, nil
}

func Decode(r io.Reader) (*Result, error) {
	var res Result
	if err := json.NewDecoder(r).Decode(&res); err != nil {
		return nil, fmt.Errorf("perception: decode: %w", err)
	}
	if !frameHash.MatchString(res.FrameHash) {
		return nil, fmt.Errorf("perception: bad frame_hash %q", res.FrameHash)
	}
	for i, p := range res.Ports {
		if p.Num != i+1 {
			return nil, fmt.Errorf("perception: port at position %d numbered %d", i+1, p.Num)
		}
		if p.Box[2] <= p.Box[0] || p.Box[3] <= p.Box[1] {
			return nil, fmt.Errorf("perception: port %d has an empty box", p.Num)
		}
	}
	if len(res.Ports) > 0 && res.Row[1] <= res.Row[0] {
		return nil, fmt.Errorf("perception: empty port row band %v", res.Row)
	}
	for i, l := range res.LEDs {
		if l.Num != i+1 || l.ActivityEnergy < 0 {
			return nil, fmt.Errorf("perception: bad LED entry at position %d", i+1)
		}
	}
	if len(res.LEDs) != 0 && len(res.LEDs) != len(res.Ports) {
		return nil, fmt.Errorf("perception: %d LEDs for %d ports", len(res.LEDs), len(res.Ports))
	}
	return &res, nil
}

type TrackFrame struct {
	Err    string
	T      time.Time
	Hand   *[4]float64
	Source string
	Score  float64
	Frozen bool
}

func (c *Client) Track(ctx context.Context, row [2]float64, fps float64) (<-chan TrackFrame, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/track?row=%g,%g&fps=%g", c.BaseURL, row[0], row[1], fps), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("perception: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		return nil, fmt.Errorf("perception track: %s: %s", resp.Status, bytes.TrimSpace(msg))
	}
	frames := make(chan TrackFrame)
	go func() {
		defer close(frames)
		defer resp.Body.Close()
		scan := bufio.NewScanner(resp.Body)
		for scan.Scan() {
			var line struct {
				T      float64     `json:"t"`
				Hand   *[4]float64 `json:"hand"`
				Source string      `json:"source"`
				Score  float64     `json:"score"`
				Frozen bool        `json:"frozen"`
				Error  string      `json:"error"`
			}
			if err := json.Unmarshal(scan.Bytes(), &line); err != nil || line.Error != "" {
				msg := line.Error
				if err != nil {
					msg = "bad track line: " + err.Error()
				}
				select {
				case frames <- TrackFrame{Err: msg}:
				case <-ctx.Done():
				}
				return
			}
			if line.Hand != nil && (line.Hand[2] <= line.Hand[0] || line.Hand[3] <= line.Hand[1]) {
				line.Hand = nil
			}
			f := TrackFrame{T: time.UnixMilli(int64(line.T * 1000)), Hand: line.Hand, Source: line.Source, Score: line.Score, Frozen: line.Frozen}
			select {
			case frames <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
	return frames, nil
}

func (c *Client) Clip(ctx context.Context) (string, error) {
	body, err := c.post(ctx, "/track/clip", nil, "")
	if err != nil {
		return "", err
	}
	defer body.Close()
	var res struct {
		Hash string `json:"hash"`
	}
	if err := json.NewDecoder(io.LimitReader(body, maxResponseBytes)).Decode(&res); err != nil {
		return "", fmt.Errorf("perception: decode clip: %w", err)
	}
	if !frameHash.MatchString(res.Hash) {
		return "", fmt.Errorf("perception: bad clip hash %q", res.Hash)
	}
	return res.Hash, nil
}
