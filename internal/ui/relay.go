package ui

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/triadstudios/owl/internal/perception"
	"github.com/triadstudios/owl/internal/reconcile"
)

const (
	maxMessage      = 2 << 20
	framesPerSecond = 10
	maxFrameSide    = 8192
	maxUtterance    = 1000
	relayIdle       = 2 * time.Minute
	pingEvery       = 30 * time.Second
)

var verdictColor = map[string]string{
	"safe":         "#2ecc71",
	"caution":      "#f1c40f",
	"do-not-touch": "#e74c3c",
	"unknown":      "#95a5a6",
}

type relayMsg struct {
	Type string `json:"type"`
	TS   int64  `json:"ts"`
	JPEG string `json:"jpeg_b64"`
	Text string `json:"text"`
}

type sayMsg struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type polygonJSON struct {
	Port   int          `json:"port"`
	Color  string       `json:"color"`
	Points [][2]float64 `json:"points"`
}

type labelJSON struct {
	Port    int    `json:"port"`
	Text    string `json:"text"`
	Verdict string `json:"verdict"`
}

type overlayMsg struct {
	Type     string        `json:"type"`
	Polygons []polygonJSON `json:"polygons"`
	Labels   []labelJSON   `json:"labels"`
}

type bucket struct {
	tokens float64
	at     time.Time
}

func (s *Server) allowFrame(device string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.buckets[device]
	if b == nil {
		b = &bucket{tokens: framesPerSecond, at: now}
		s.buckets[device] = b
	}
	b.tokens = min(framesPerSecond, b.tokens+now.Sub(b.at).Seconds()*framesPerSecond)
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (s *Server) relay(w http.ResponseWriter, r *http.Request) {
	device, _ := r.Context().Value(ctxKey{}).(string)
	c, err := upgrade(w, r)
	if err != nil {
		s.Log.Warn("relay upgrade", "device", device, "err", err)
		return
	}
	s.mu.Lock()
	s.relays++
	s.mu.Unlock()
	s.Log.Info("relay connected", "device", device, "remote", r.RemoteAddr)

	ctx, cancel := context.WithCancel(r.Context())
	frames := make(chan []byte, 1)
	var wg sync.WaitGroup
	wg.Go(func() { s.overlays(ctx, c, device, frames) })
	wg.Go(func() {
		t := time.NewTicker(pingEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := c.write(opPing, nil); err != nil {
					return
				}
			}
		}
	})
	code, reason, dropped := s.readRelay(c, device, frames)
	cancel()
	close(frames)
	if err := c.close(code, reason); err != nil && !errors.Is(err, net.ErrClosed) {
		s.Log.Debug("relay close", "device", device, "err", err)
	}
	wg.Wait()
	s.mu.Lock()
	s.relays--
	s.mu.Unlock()
	s.Log.Info("relay disconnected", "device", device, "code", code, "reason", reason, "frames_dropped_rate", dropped)
}

func (s *Server) readRelay(c *wsConn, device string, frames chan []byte) (uint16, string, int) {
	dropped := 0
	for {
		op, data, err := c.read(maxMessage, relayIdle)
		switch {
		case errors.Is(err, errTooBig):
			return closeTooBig, "message over 2 MB", dropped
		case errors.Is(err, errProtocol):
			return 1002, "protocol error", dropped
		case errors.Is(err, io.EOF):
			return 1000, "client closed", dropped
		case err != nil:
			return 1001, "read failed", dropped
		}
		if op != opText {
			return closeUnsupported, "text messages only", dropped
		}
		var m relayMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return closeBadData, "bad json", dropped
		}
		switch m.Type {
		case "frame":
			if !s.allowFrame(device, time.Now()) {
				dropped++
				continue
			}
			frame, err := base64.StdEncoding.DecodeString(m.JPEG)
			if err != nil || len(frame) == 0 {
				return closeBadData, "frame needs jpeg_b64", dropped
			}
			s.mu.Lock()
			s.lastFrame = time.Now()
			s.mu.Unlock()
			select {
			case <-frames:
			default:
			}
			frames <- frame
		case "utterance":
			text := strings.TrimSpace(m.Text)
			if text == "" || len(text) > maxUtterance {
				return closeBadData, "utterance needs text of at most 1000 bytes", dropped
			}
			s.Log.Info("utterance", "device", device, "text", text)
			if err := s.send(c, sayMsg{Type: "say", Text: "not yet"}); err != nil {
				return 1001, "write failed", dropped
			}
		default:
			t := m.Type
			if len(t) > 32 {
				t = t[:32]
			}
			s.Log.Warn("relay rejected message", "device", device, "type", t)
			return closePolicy, "unknown message type", dropped
		}
	}
}

func (s *Server) overlays(ctx context.Context, c *wsConn, device string, frames <-chan []byte) {
	for frame := range frames {
		start := time.Now()
		msg, err := s.frameOverlay(ctx, frame)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			s.Log.Warn("frame rejected", "device", device, "bytes", len(frame), "took_ms", time.Since(start).Milliseconds(), "err", err)
			// A stale overlay would sit on whatever cable the phone points at now.
			msg = overlayMsg{Type: "overlay", Polygons: []polygonJSON{}, Labels: []labelJSON{}}
		} else {
			s.Log.Info("frame", "device", device, "bytes", len(frame), "ports", len(msg.Labels), "took_ms", time.Since(start).Milliseconds())
		}
		if err := s.send(c, msg); err != nil {
			s.Log.Warn("relay send", "device", device, "err", err)
			return
		}
	}
}

func (s *Server) frameOverlay(ctx context.Context, frame []byte) (overlayMsg, error) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(frame))
	if err != nil {
		return overlayMsg{}, fmt.Errorf("not a JPEG: %w", err)
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > maxFrameSide || cfg.Height > maxFrameSide {
		return overlayMsg{}, fmt.Errorf("frame %dx%d outside 1..%d px", cfg.Width, cfg.Height, maxFrameSide)
	}
	res, err := s.Perception.Segment(ctx, frame, "image/jpeg")
	if err != nil {
		return overlayMsg{}, err
	}
	ports, err := s.reconcile(res)
	if err != nil {
		return overlayMsg{}, err
	}
	return overlay(ports, res, float64(cfg.Width), float64(cfg.Height)), nil
}

func overlay(ports []reconcile.Port, cam *perception.Result, width, height float64) overlayMsg {
	msg := overlayMsg{Type: "overlay", Polygons: []polygonJSON{}, Labels: []labelJSON{}}
	byNum := map[int]*reconcile.Port{}
	for i := range ports {
		byNum[ports[i].Num] = &ports[i]
	}
	norm := func(x, y float64) [2]float64 {
		return [2]float64{min(max(x/width, 0), 1), min(max(y/height, 0), 1)}
	}
	for _, c := range cam.Ports {
		card := newCard(nil)
		if p := byNum[c.Num]; p != nil {
			card = newCard(p.Blast)
		}
		color := verdictColor[card.Verdict]
		b := c.Box
		msg.Polygons = append(msg.Polygons, polygonJSON{Port: c.Num, Color: color, Points: [][2]float64{norm(b[0], b[1]), norm(b[2], b[1]), norm(b[2], b[3]), norm(b[0], b[3])}})
		if len(c.CablePolygon) > 2 {
			pts := make([][2]float64, len(c.CablePolygon))
			for i, p := range c.CablePolygon {
				pts[i] = norm(p[0], p[1])
			}
			msg.Polygons = append(msg.Polygons, polygonJSON{Port: c.Num, Color: color, Points: pts})
		}
		short, _, _ := strings.Cut(card.Downstream, ":")
		msg.Labels = append(msg.Labels, labelJSON{Port: c.Num, Text: strings.TrimSpace(fmt.Sprintf("%d %s", c.Num, short)), Verdict: card.Verdict})
	}
	return msg
}

func (s *Server) send(c *wsConn, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.write(opText, data)
}
