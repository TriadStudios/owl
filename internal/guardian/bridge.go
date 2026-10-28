package guardian

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

const maxBridgeReply = 64 << 10

type Bridge struct {
	BaseURL string
	HTTP    *http.Client
	Log     *slog.Logger
}

type motionReply struct {
	Moving   bool       `json:"moving"`
	Centroid [2]float64 `json:"centroid"`
}

func (b *Bridge) call(method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, b.BaseURL+path, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("robot bridge %s: %w", path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBridgeReply))
	if err != nil {
		return fmt.Errorf("robot bridge %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("robot bridge %s: %s: %s", path, resp.Status, bytes.TrimSpace(data))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("robot bridge %s: %w", path, err)
	}
	return nil
}

func (b *Bridge) Turn(to Point) {
	if err := b.call(http.MethodPost, "/turn", map[string]float64{"x": to.X, "y": to.Y}, nil); err != nil {
		b.Log.Error("turn", "err", err)
	}
}

func (b *Bridge) Home() {
	if err := b.call(http.MethodPost, "/home", nil, nil); err != nil {
		b.Log.Error("home", "err", err)
	}
}

func (b *Bridge) Say(text string) {
	b.Log.Info("say", "text", text)
	if err := b.call(http.MethodPost, "/say", map[string]string{"text": text}, nil); err != nil {
		b.Log.Error("say", "err", err)
	}
}

func (b *Bridge) Listen() string {
	var reply struct {
		Text string `json:"text"`
	}
	if err := b.call(http.MethodPost, "/listen", nil, &reply); err != nil {
		b.Log.Error("listen", "err", err)
		return ""
	}
	b.Log.Info("heard", "text", reply.Text)
	return reply.Text
}

func (b *Bridge) motion() motionReply {
	var reply motionReply
	if err := b.call(http.MethodGet, "/motion", nil, &reply); err != nil {
		b.Log.Error("motion", "err", err)
		return motionReply{}
	}
	return reply
}

func (b *Bridge) Motion() bool {
	return b.motion().Moving
}

func (b *Bridge) Frame() Frame {
	c := b.motion().Centroid
	return Frame{Centroid: Point{c[0], c[1]}}
}
