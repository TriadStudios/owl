package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/triadstudios/owl/internal/discovery"
	"github.com/triadstudios/owl/internal/guardian"
	"github.com/triadstudios/owl/internal/narrate"
	"github.com/triadstudios/owl/internal/perception"
	"github.com/triadstudios/owl/internal/policy"
)

func fixtureServer(t *testing.T) (*Server, string) {
	t.Helper()
	devices := filepath.Join(t.TempDir(), "devices.json")
	token, err := Pair(devices, "phone", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rf, err := os.Open("../../policy/rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	rules, err := policy.Load(rf)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		LeasesPath:     "../../testdata/leases.csv",
		Rules:          rules,
		AttributionMin: 0.6,
		AggregationMin: 3,
		FindingsPath:   filepath.Join(t.TempDir(), "findings.json"),
		Devices:        &Registry{Path: devices},
		Log:            slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, token
}

func TestPairedTokenAcceptedOthersRejectedBeforeBody(t *testing.T) {
	s, token := fixtureServer(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	get := func(auth string) int {
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/findings", nil)
		if err != nil {
			t.Fatal(err)
		}
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := get("Bearer " + token); code != http.StatusOK {
		t.Fatalf("paired token: status %d, want 200", code)
	}
	for _, auth := range []string{"", "Bearer ", "Bearer " + token[:len(token)-1] + "x", token} {
		if code := get(auth); code != http.StatusUnauthorized {
			t.Errorf("Authorization %q: status %d, want 401", auth, code)
		}
	}

	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.WriteString(conn, "POST /relay HTTP/1.1\r\nHost: owl\r\nAuthorization: Bearer nope\r\nContent-Length: 10000000\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no response before the body was sent: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unpaired POST: status %d, want 401", resp.StatusCode)
	}
	if _, err := conn.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("connection still open after rejecting: %v", err)
	}

	if err := Revoke(s.Devices.Path, "phone"); err != nil {
		t.Fatal(err)
	}
	if code := get("Bearer " + token); code != http.StatusUnauthorized {
		t.Errorf("revoked token: status %d, want 401", code)
	}
}

func loadFixtureTable(t *testing.T, s *Server) *perception.Result {
	t.Helper()
	cf, err := os.Open("../../testdata/perception-switch-face.json")
	if err != nil {
		t.Fatal(err)
	}
	defer cf.Close()
	cam, err := perception.Decode(cf)
	if err != nil {
		t.Fatal(err)
	}
	wf, err := os.Open("../../testdata/walk-datto.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer wf.Close()
	vars, err := discovery.ParseWalk(wf)
	if err != nil {
		t.Fatal(err)
	}
	state, err := discovery.Build(vars, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s.setSwitch(state, state, nil)
	if _, err := s.reconcile(cam); err != nil {
		t.Fatal(err)
	}
	return cam
}

func TestPortsTableOnFixtures(t *testing.T) {
	s, token := fixtureServer(t)
	cam := loadFixtureTable(t, s)

	req := httptest.NewRequest(http.MethodGet, "/api/ports", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var table struct {
		FrameHash string `json:"frame_hash"`
		Ports     []struct {
			Num     int      `json:"num"`
			Status  string   `json:"status"`
			Verdict string   `json:"verdict"`
			Sources []string `json:"sources"`
			Devices []struct {
				MAC     string   `json:"mac"`
				Sources []string `json:"sources"`
			} `json:"devices"`
		} `json:"ports"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &table); err != nil {
		t.Fatal(err)
	}
	if table.FrameHash != cam.FrameHash {
		t.Errorf("frame_hash %q, want %q", table.FrameHash, cam.FrameHash)
	}
	want := map[int]string{1: "do-not-touch", 2: "do-not-touch", 3: "safe", 9: "unknown"}
	seen := map[int]bool{}
	for _, p := range table.Ports {
		seen[p.Num] = true
		if p.Num <= 8 && p.Status != "consistent" {
			t.Errorf("port %d status %q, want consistent", p.Num, p.Status)
		}
		if v, ok := want[p.Num]; ok && p.Verdict != v {
			t.Errorf("port %d verdict %q, want %q", p.Num, p.Verdict, v)
		}
		if len(p.Sources) == 0 {
			t.Errorf("port %d has no sources", p.Num)
		}
		for _, d := range p.Devices {
			if !strings.Contains(d.MAC, ":") || d.Sources == nil {
				t.Errorf("port %d device %+v: want a MAC and a sources list", p.Num, d)
			}
		}
	}
	for n := 1; n <= 8; n++ {
		if !seen[n] {
			t.Errorf("port %d missing from /api/ports", n)
		}
	}
}

type fakeNarrator struct {
	reply narrate.Reply
	hand  int
}

func (f *fakeNarrator) Reply(_ context.Context, req narrate.Request) (narrate.Reply, error) {
	if req.Scene != nil {
		f.hand = req.Scene.HandPort
	}
	return f.reply, nil
}

func TestRelayAnswerNeverContradictsPolicy(t *testing.T) {
	s, _ := fixtureServer(t)
	loadFixtureTable(t, s)
	template := guardian.Line(2, s.ports, "")
	cases := []struct {
		name  string
		heard string
		hand  int
		model *fakeNarrator
		want  string
	}{
		{"model calls a do-not-touch port safe", "can I pull port 2", 0, &fakeNarrator{reply: narrate.Reply{Port: 2, Line: "Port 2 is safe, go ahead."}}, template},
		{"model agrees with policy", "what about port 3", 0, &fakeNarrator{reply: narrate.Reply{Port: 3, Line: "Port 3 is safe, go ahead."}}, "Port 3 is safe, go ahead."},
		{"model talks about another port than the one said", "pulling port 2", 0, &fakeNarrator{reply: narrate.Reply{Port: 3, Line: "Port 3 is safe."}}, template},
		{"no narrator", "pulling port 2", 0, nil, template},
		{"model stays quiet", "hmm let me think", 0, &fakeNarrator{}, ""},
		{"this one is the port under the hand", "can I pull this one", 2, nil, template},
		{"model calls the port under the hand safe", "can I pull this one", 2, &fakeNarrator{reply: narrate.Reply{Port: 2, Line: "Port 2 is safe."}}, template},
	}
	for _, c := range cases {
		s.Narrator = nil
		if c.model != nil {
			s.Narrator = c.model
		}
		if got := s.answer(context.Background(), "phone", c.heard, c.hand, nil, map[int]bool{}); got != c.want {
			t.Errorf("%s: said %q, want %q", c.name, got, c.want)
		}
		if c.model != nil && c.model.hand != c.hand {
			t.Errorf("%s: narrator saw hand on port %d, want %d", c.name, c.model.hand, c.hand)
		}
	}
}

func TestRelayHandLineAlwaysSaysThePortAndItsVerdict(t *testing.T) {
	s, _ := fixtureServer(t)
	loadFixtureTable(t, s)
	template := guardian.Line(2, s.ports, "")
	cases := []struct {
		name  string
		model *fakeNarrator
		want  string
	}{
		{"model says the port and its verdict", &fakeNarrator{reply: narrate.Reply{Port: 2, Line: "Port 2 is do not touch, that's the uplink."}}, "Port 2 is do not touch, that's the uplink."},
		{"model talks about another port", &fakeNarrator{reply: narrate.Reply{Port: 3, Line: "Port 3 is safe."}}, template},
		{"model leaves out the verdict", &fakeNarrator{reply: narrate.Reply{Port: 2, Line: "Port 2 has the uplink on it."}}, template},
		{"no narrator", nil, template},
	}
	for _, c := range cases {
		s.Narrator = nil
		if c.model != nil {
			s.Narrator = c.model
		}
		if got := s.handLine(context.Background(), "phone", 2, nil, map[int]bool{}); got != c.want {
			t.Errorf("%s: said %q, want %q", c.name, got, c.want)
		}
	}
}

func TestWorkOrderIsCheckedAgainstTheLiveSwitch(t *testing.T) {
	s, token := fixtureServer(t)
	s.JobsPath = filepath.Join(t.TempDir(), "jobs.json")
	loadFixtureTable(t, s)
	call := func(method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	cases := []struct {
		body     string
		port     float64
		mismatch bool
		check    []string
	}{
		{`{"ticket":"WO-1","device":"closet-box","claimed_port":1,"tech":"dillon"}`, 3, true, []string{"ticket says port 1", "closet-box on port 3", "Don't touch port 1", "do not touch"}},
		{`{"ticket":"WO-2","device":"Closet-Box","claimed_port":3,"tech":"dillon"}`, 3, false, []string{"Closet-Box is on port 3", "safe"}},
		{`{"ticket":"WO-3","device":"192.0.2.15","tech":"dillon"}`, 1, false, []string{"on port 1", "do not touch"}},
		{`{"ticket":"WO-4","device":"nas-99","claimed_port":5,"tech":"dillon"}`, 5, false, []string{"can't find nas-99", "port 5"}},
	}
	for _, c := range cases {
		code, out := call(http.MethodPost, "/api/workorders", c.body)
		if code != http.StatusCreated {
			t.Fatalf("%s: status %d %v", c.body, code, out)
		}
		if out["port"] != c.port || out["mismatch"] != c.mismatch {
			t.Errorf("%s: port %v mismatch %v, want %v %v", c.body, out["port"], out["mismatch"], c.port, c.mismatch)
		}
		for _, want := range c.check {
			if !strings.Contains(out["check"].(string), want) {
				t.Errorf("%s: check %q lacks %q", c.body, out["check"], want)
			}
		}
	}

	if code, _ := call(http.MethodPost, "/api/workorders", `{"device":"nas-99","tech":"dillon"}`); code != http.StatusUnprocessableEntity {
		t.Errorf("unknown device with no port: status %d, want 422", code)
	}
	s.witness(3, "can I pull port three", "Port 3 is safe.")
	code, out := call(http.MethodGet, "/api/workorders/1", "")
	if code != http.StatusOK || out["device_port_now"] != float64(3) || len(out["events"].([]any)) != 1 {
		t.Errorf("detail: status %d %v", code, out)
	}
	if code, out := call(http.MethodPost, "/api/workorders/1/close", ""); code != http.StatusOK || out["status"] != "closed" {
		t.Errorf("close: status %d %v", code, out)
	}
	if code, _ := call(http.MethodPost, "/api/workorders/1/close", ""); code != http.StatusConflict {
		t.Errorf("second close: status %d, want 409", code)
	}
}

func TestWorkOrderIsAcceptedOnceByApiOrVoice(t *testing.T) {
	s, token := fixtureServer(t)
	s.JobsPath = filepath.Join(t.TempDir(), "jobs.json")
	loadFixtureTable(t, s)
	call := func(method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	for _, body := range []string{
		`{"ticket":"WO-1042","device":"closet-box","claimed_port":1}`,
		`{"ticket":"WO-1043","device":"Home-AP"}`,
		`{"ticket":"WO-1044","device":"closet-box","tech":"sam"}`,
	} {
		if code, out := call(http.MethodPost, "/api/workorders", body); code != http.StatusCreated {
			t.Fatalf("%s: %d %v", body, code, out)
		}
	}
	if _, out := call(http.MethodGet, "/api/workorders/1", ""); out["status"] != "open" {
		t.Errorf("unassigned order status %v, want open", out["status"])
	}

	if line, ok := s.acceptByVoice("dillon", "can I pull port five"); ok {
		t.Errorf("a port question was taken as accepting: %q", line)
	}
	if line, _ := s.acceptByVoice("dillon", "accept the work order"); !strings.Contains(line, "WO-1042, WO-1043") {
		t.Errorf("two free orders, none named: %q", line)
	}
	line, _ := s.acceptByVoice("dillon", "I'll take work order 1042")
	if !strings.HasPrefix(line, "Accepted WO-1042.") || !strings.Contains(line, "closet-box on port 3") {
		t.Errorf("accept by voice: %q", line)
	}
	if code, out := call(http.MethodPost, "/api/workorders/1/accept", `{"tech":"sam"}`); code != http.StatusConflict {
		t.Errorf("second tech accepting: %d %v", code, out)
	}
	if code, out := call(http.MethodPost, "/api/workorders/3/accept", `{"tech":"dillon"}`); code != http.StatusConflict {
		t.Errorf("accepting an order assigned to someone else: %d %v", code, out)
	}
	if code, out := call(http.MethodPost, "/api/workorders/3/accept", `{"tech":"Sam"}`); code != http.StatusOK || out["status"] != "accepted" {
		t.Errorf("assigned tech accepting: %d %v", code, out)
	}
	if code, out := call(http.MethodPost, "/api/workorders/2/accept", ""); code != http.StatusOK || out["tech"] != "phone" {
		t.Errorf("accepting with no name falls back to the paired device: %d %v", code, out)
	}
}
