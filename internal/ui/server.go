package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/triadstudios/owl/internal/blast"
	"github.com/triadstudios/owl/internal/discovery"
	"github.com/triadstudios/owl/internal/finding"
	"github.com/triadstudios/owl/internal/perception"
	"github.com/triadstudios/owl/internal/policy"
	"github.com/triadstudios/owl/internal/reconcile"
)

const (
	pollEvery    = 60 * time.Second
	probeTimeout = 2 * time.Second
)

var (
	errNoPoll    = errors.New("no switch poll yet")
	evidenceHash = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Config struct {
	Switch         discovery.Config
	Perception     *perception.Client
	RobotURL       string
	LeasesPath     string
	Rules          policy.Rules
	Tags           blast.Tagger
	AttributionMin float64
	AggregationMin int
	EvidenceDir    string
	FindingsPath   string
	Devices        *Registry
	Log            *slog.Logger
}

type Server struct {
	Config
	probe *http.Client

	mu        sync.Mutex
	prev, cur *discovery.SwitchState
	ids       *discovery.Identities
	tags      blast.Tagger
	camera    *perception.Result
	ports     []reconcile.Port
	gen       int
	portsGen  int
	switchOK  bool
	lastPoll  time.Time
	lastFrame time.Time
	relays    int
	buckets   map[string]*bucket
}

func New(cfg Config) (*Server, error) {
	ids, err := loadIdentities(cfg.LeasesPath)
	if err != nil {
		return nil, err
	}
	return &Server{
		Config:  cfg,
		probe:   &http.Client{Timeout: probeTimeout},
		ids:     ids,
		tags:    newTagCache(cfg.Tags),
		camera:  &perception.Result{},
		buckets: map[string]*bucket{},
	}, nil
}

func (s *Server) SetCamera(res *perception.Result) {
	s.mu.Lock()
	s.camera = res
	s.mu.Unlock()
}

func (s *Server) Run(ctx context.Context) {
	for {
		start := time.Now()
		s.poll(ctx)
		stale, err := StalePairCodes(s.Devices.Path, start)
		if err != nil {
			s.Log.Error("pairing QR check", "err", err)
		}
		for _, code := range stale {
			s.Log.Warn("unused pairing QR older than 10 min holds a live token; scan it or delete it", "path", code)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(start.Add(pollEvery))):
		}
	}
}

func (s *Server) poll(ctx context.Context) {
	start := time.Now()
	prev, cur, err := pollTwice(ctx, s.Switch, s.Rules.LivenessWindow)
	if err != nil {
		s.mu.Lock()
		s.switchOK = false
		s.mu.Unlock()
		s.Log.Error("snmp poll", "err", err)
		return
	}
	ids, err := loadIdentities(s.LeasesPath)
	if err != nil {
		s.Log.Error("leases", "err", err)
	}
	s.setSwitch(prev, cur, ids)
	ports, err := s.reconcile(nil)
	if err != nil {
		s.Log.Error("reconcile", "err", err)
		return
	}
	s.Log.Info("snmp poll", "ports", len(cur.Ports), "took_ms", time.Since(start).Milliseconds(), "verdicts", verdicts(ports))
}

func (s *Server) setSwitch(prev, cur *discovery.SwitchState, ids *discovery.Identities) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prev, s.cur, s.switchOK, s.lastPoll = prev, cur, true, cur.Polled
	if ids != nil {
		s.ids = ids
	}
	s.tags = newTagCache(s.Tags)
}

func (s *Server) inputs() blast.Inputs {
	return blast.Inputs{
		Prev:           s.prev,
		Cur:            s.cur,
		Camera:         s.camera,
		AttributionMin: s.AttributionMin,
		AggregationMin: s.AggregationMin,
		IDs:            s.ids,
		Tags:           s.tags,
		Rules:          s.Rules,
	}
}

func (s *Server) reconcile(camera *perception.Result) ([]reconcile.Port, error) {
	s.mu.Lock()
	if camera != nil {
		s.camera = camera
	}
	in := s.inputs()
	s.gen++
	gen := s.gen
	s.mu.Unlock()
	if in.Cur == nil {
		return nil, errNoPoll
	}
	ports, err := reconcile.Join(in)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if gen > s.portsGen {
		s.ports, s.portsGen = ports, gen
	}
	s.mu.Unlock()
	return ports, nil
}

func pollTwice(ctx context.Context, cfg discovery.Config, window time.Duration) (*discovery.SwitchState, *discovery.SwitchState, error) {
	prev, err := discovery.Poll(cfg)
	if err != nil {
		return nil, nil, err
	}
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-time.After(window):
	}
	cur, err := discovery.Poll(cfg)
	if err != nil {
		return nil, nil, err
	}
	return prev, cur, nil
}

func loadIdentities(path string) (*discovery.Identities, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	ids, err := discovery.LoadIdentities(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return ids, nil
}

func verdicts(ports []reconcile.Port) map[string]string {
	out := map[string]string{}
	for _, p := range ports {
		out[strconv.Itoa(p.Num)] = newCard(p.Blast).Verdict
	}
	return out
}

type tagHit struct {
	tags  []string
	found bool
}

// NetBox tags are re-read once per poll; without the cache every relay frame costs a NetBox round trip per MAC.
type tagCache struct {
	inner blast.Tagger
	mu    sync.Mutex
	hits  map[string]tagHit
}

func newTagCache(t blast.Tagger) blast.Tagger {
	if t == nil {
		return nil
	}
	return &tagCache{inner: t, hits: map[string]tagHit{}}
}

func (t *tagCache) Tags(mac string) ([]string, bool, error) {
	t.mu.Lock()
	h, ok := t.hits[mac]
	t.mu.Unlock()
	if ok {
		return h.tags, h.found, nil
	}
	tags, found, err := t.inner.Tags(mac)
	if err != nil {
		return nil, false, err
	}
	t.mu.Lock()
	t.hits[mac] = tagHit{tags, found}
	t.mu.Unlock()
	return tags, found, nil
}

type ctxKey struct{}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/ports", s.portsTable)
	mux.HandleFunc("GET /api/blast/{port}", s.blastCard)
	mux.HandleFunc("GET /api/findings", s.findings)
	mux.HandleFunc("GET /api/evidence/{hash}", s.evidence)
	mux.HandleFunc("GET /relay", s.relay)
	return s.auth(mux)
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		var name string
		if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && token != "" {
			var err error
			if name, err = s.Devices.Authenticate(token, start); err != nil {
				s.Log.Error("device registry", "err", err)
			}
		}
		if name == "" {
			s.Log.Warn("unauthorized", "remote", r.RemoteAddr, "method", r.Method, "path", r.URL.Path)
			reject(w)
			return
		}
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), ctxKey{}, name)))
		s.Log.Info("request", "device", name, "method", r.Method, "path", r.URL.Path, "status", sw.status, "took_ms", time.Since(start).Milliseconds())
	})
}

// With Connection: close net/http drops the connection after the reply instead of draining the unread body.
func reject(w http.ResponseWriter) {
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusUnauthorized)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("connection cannot be hijacked")
	}
	w.status = http.StatusSwitchingProtocols
	return hj.Hijack()
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.Log.Warn("write response", "err", err)
	}
}

func (s *Server) fail(w http.ResponseWriter, status int, msg string) {
	s.writeJSON(w, status, map[string]string{"error": msg})
}

type healthJSON struct {
	SwitchReachable bool     `json:"switch_reachable"`
	PerceptionUp    bool     `json:"perception_up"`
	RobotAttached   bool     `json:"robot_attached"`
	RelayConnected  int      `json:"relay_connected"`
	LastPollAgeS    *float64 `json:"last_poll_age_s"`
	LastFrameAgeS   *float64 `json:"last_frame_age_s"`
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	var h healthJSON
	var wg sync.WaitGroup
	wg.Go(func() { h.PerceptionUp = s.reachable(r.Context(), s.Perception.BaseURL+"/health") })
	wg.Go(func() { h.RobotAttached = s.RobotURL != "" && s.reachable(r.Context(), s.RobotURL+"/health") })
	wg.Wait()
	now := time.Now()
	s.mu.Lock()
	h.SwitchReachable, h.RelayConnected = s.switchOK, s.relays
	h.LastPollAgeS, h.LastFrameAgeS = age(s.lastPoll, now), age(s.lastFrame, now)
	s.mu.Unlock()
	s.writeJSON(w, http.StatusOK, h)
}

func (s *Server) reachable(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := s.probe.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode == http.StatusOK
}

func age(t, now time.Time) *float64 {
	if t.IsZero() {
		return nil
	}
	v := math.Round(now.Sub(t).Seconds()*10) / 10
	return &v
}

type deviceJSON struct {
	MAC      string   `json:"mac"`
	IP       string   `json:"ip,omitempty"`
	Hostname string   `json:"hostname,omitempty"`
	Vendor   string   `json:"vendor,omitempty"`
	Online   bool     `json:"online"`
	Sources  []string `json:"sources"`
}

type cardJSON struct {
	Verdict       string       `json:"verdict"`
	UnknownReason string       `json:"unknown_reason,omitempty"`
	Downstream    string       `json:"downstream"`
	Behind        int          `json:"behind"`
	Neighbor      string       `json:"neighbor,omitempty"`
	Liveness      string       `json:"liveness,omitempty"`
	DeltaBytes    uint64       `json:"delta_bytes"`
	WindowS       float64      `json:"window_s"`
	Criticality   []string     `json:"criticality"`
	Devices       []deviceJSON `json:"devices"`
	Inputs        []string     `json:"inputs"`
}

type portJSON struct {
	Num     int      `json:"num"`
	Camera  string   `json:"camera"`
	Switch  string   `json:"switch"`
	Status  string   `json:"status"`
	Sources []string `json:"sources"`
	cardJSON
}

type portsJSON struct {
	PolledAt  time.Time  `json:"polled_at"`
	FrameHash string     `json:"frame_hash,omitempty"`
	Ports     []portJSON `json:"ports"`
}

type blastJSON struct {
	Port     int       `json:"port"`
	PolledAt time.Time `json:"polled_at"`
	cardJSON
}

func newCard(c *blast.Card) cardJSON {
	if c == nil {
		return cardJSON{Verdict: "unknown", UnknownReason: "port not on the switch", Criticality: []string{}, Devices: []deviceJSON{}, Inputs: []string{}}
	}
	out := cardJSON{
		Verdict:       string(c.Verdict),
		UnknownReason: c.Unknown,
		Downstream:    c.Downstream,
		Behind:        c.Behind,
		Neighbor:      c.Neighbor,
		Liveness:      string(c.Liveness),
		DeltaBytes:    c.DeltaBytes,
		WindowS:       c.Window.Seconds(),
		Criticality:   orEmpty(c.Criticality),
		Devices:       []deviceJSON{},
		Inputs:        orEmpty(c.Inputs),
	}
	if c.Unknown != "" {
		out.Verdict = "unknown"
	}
	for _, d := range c.Devices {
		out.Devices = append(out.Devices, deviceJSON{MAC: d.MAC, IP: d.IP, Hostname: d.Hostname, Vendor: d.Vendor, Online: d.Online, Sources: orEmpty(d.Sources)})
	}
	return out
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func (s *Server) portsTable(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	cur, camera, ports := s.cur, s.camera, s.ports
	s.mu.Unlock()
	if cur == nil {
		s.fail(w, http.StatusServiceUnavailable, errNoPoll.Error())
		return
	}
	out := portsJSON{PolledAt: cur.Polled, FrameHash: camera.FrameHash, Ports: []portJSON{}}
	for _, p := range ports {
		out.Ports = append(out.Ports, portJSON{Num: p.Num, Camera: orUnknown(p.Camera), Switch: orUnknown(p.Switch), Status: string(p.Status), Sources: orEmpty(p.Sources), cardJSON: newCard(p.Blast)})
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) blastCard(w http.ResponseWriter, r *http.Request) {
	num, err := strconv.Atoi(r.PathValue("port"))
	if err != nil || num < 1 {
		s.fail(w, http.StatusBadRequest, "port must be a positive integer")
		return
	}
	s.mu.Lock()
	in := s.inputs()
	s.mu.Unlock()
	if in.Cur == nil {
		s.fail(w, http.StatusServiceUnavailable, errNoPoll.Error())
		return
	}
	if _, ok := in.Cur.Ports[num]; !ok {
		s.fail(w, http.StatusNotFound, fmt.Sprintf("port %d not on the switch", num))
		return
	}
	if in.Prev, in.Cur, err = pollTwice(r.Context(), s.Switch, s.Rules.LivenessWindow); err != nil {
		s.Log.Error("blast poll", "port", num, "err", err)
		s.fail(w, http.StatusBadGateway, "switch poll failed")
		return
	}
	card, err := blast.Assess(num, in)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := blastJSON{Port: num, PolledAt: in.Cur.Polled, cardJSON: newCard(&card)}
	s.Log.Info("policy decision", "device", r.Context().Value(ctxKey{}), "port", num, "verdict", out.Verdict, "inputs", card.Inputs)
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) findings(w http.ResponseWriter, r *http.Request) {
	found, err := finding.Load(s.FindingsPath)
	if err != nil {
		s.Log.Error("findings", "err", err)
		s.fail(w, http.StatusInternalServerError, "cannot read findings")
		return
	}
	if found == nil {
		found = []finding.Finding{}
	}
	s.writeJSON(w, http.StatusOK, map[string][]finding.Finding{"findings": found})
}

func (s *Server) evidence(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if !evidenceHash.MatchString(hash) {
		s.fail(w, http.StatusBadRequest, "evidence is addressed by a lowercase sha256")
		return
	}
	for _, ext := range []string{".png", ".jpg", ".mp4"} {
		f, err := os.Open(filepath.Join(s.EvidenceDir, hash+ext))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			s.Log.Error("evidence", "hash", hash, "err", err)
			s.fail(w, http.StatusInternalServerError, "cannot open evidence")
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			s.fail(w, http.StatusInternalServerError, "cannot stat evidence")
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, hash+ext, info.ModTime(), f)
		return
	}
	s.fail(w, http.StatusNotFound, "no such evidence")
}
