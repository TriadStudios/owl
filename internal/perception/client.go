package perception

import (
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
	Ports     []Port `json:"ports"`
	LEDs      []LED  `json:"leds"`
	FrameHash string `json:"frame_hash"`
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

func (c *Client) Motion(ctx context.Context, regions [][4]int, window time.Duration) ([]float64, error) {
	req, err := json.Marshal(map[string]any{"regions": regions, "seconds": window.Seconds()})
	if err != nil {
		return nil, err
	}
	body, err := c.post(ctx, "/motion", req, "application/json")
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
	if len(res.Changes) != len(regions) {
		return nil, fmt.Errorf("perception: %d motion values for %d regions", len(res.Changes), len(regions))
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
