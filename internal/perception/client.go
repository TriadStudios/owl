package perception

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
)

const maxResponseBytes = 4 << 20

type Result struct {
	Ports     []Port `json:"ports"`
	FrameHash string `json:"frame_hash"`
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/segment", bytes.NewReader(image))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("perception: %w", err)
	}
	defer resp.Body.Close()
	body := io.LimitReader(resp.Body, maxResponseBytes)
	if resp.StatusCode != http.StatusOK {
		msg, err := io.ReadAll(body)
		if err != nil {
			return nil, fmt.Errorf("perception: %s (reading body: %w)", resp.Status, err)
		}
		return nil, fmt.Errorf("perception: %s: %s", resp.Status, bytes.TrimSpace(msg))
	}
	return Decode(body)
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
	return &res, nil
}
