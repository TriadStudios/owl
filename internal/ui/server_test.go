package ui

import (
	"bufio"
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

func TestPortsTableOnFixtures(t *testing.T) {
	s, token := fixtureServer(t)
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
