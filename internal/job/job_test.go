package job

import (
	"path/filepath"
	"testing"
	"time"
)

func TestParseWindow(t *testing.T) {
	now := time.Date(2026, 10, 28, 8, 0, 0, 0, time.Local)
	start, end, err := ParseWindow("09:30-11:00", now)
	if err != nil || !start.Equal(time.Date(2026, 10, 28, 9, 30, 0, 0, time.Local)) || !end.Equal(time.Date(2026, 10, 28, 11, 0, 0, 0, time.Local)) {
		t.Fatalf("09:30-11:00: %v %v %v", start, end, err)
	}
	start, end, err = ParseWindow("23:00-01:00", now)
	if err != nil || end.Sub(start) != 2*time.Hour {
		t.Fatalf("overnight window: %v %v %v", start, end, err)
	}
	for _, bad := range []string{"", "9-11", "09:30", "25:00-26:00", "2026-10-28T09:00-2026-10-28T11:00"} {
		if _, _, err := ParseWindow(bad, now); err == nil {
			t.Errorf("%q parsed, want an error", bad)
		}
	}
}

func TestAddCloseAndCovering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	t0 := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	a, err := Add(path, Job{Port: 5, Tech: "sam", Start: t0, End: t0.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Add(path, Job{Port: 7, Tech: "ana", Start: t0, End: t0.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != 1 || b.ID != 2 {
		t.Fatalf("ids %d %d, want 1 2", a.ID, b.ID)
	}
	if _, err := Add(path, Job{Port: 5, Tech: "sam", Start: t0, End: t0}); err == nil {
		t.Error("empty window accepted")
	}

	jobs, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		port int
		at   time.Time
		want int
	}{
		{"inside the window", 5, t0.Add(30 * time.Minute), 1},
		{"other port", 6, t0.Add(30 * time.Minute), 0},
		{"before the window", 5, t0.Add(-time.Minute), 0},
		{"after the window", 5, t0.Add(61 * time.Minute), 0},
	}
	for _, c := range cases {
		got := 0
		if j := Covering(jobs, c.port, c.at); j != nil {
			got = j.ID
		}
		if got != c.want {
			t.Errorf("%s: job %d, want %d", c.name, got, c.want)
		}
	}

	if err := Close(path, 1, t0.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := Close(path, 1, t0.Add(11*time.Minute)); err == nil {
		t.Error("closed job 1 twice")
	}
	if err := Close(path, 9, t0); err == nil {
		t.Error("closed a job that does not exist")
	}
	if jobs, err = Load(path); err != nil {
		t.Fatal(err)
	}
	if j := Covering(jobs, 5, t0.Add(30*time.Minute)); j != nil {
		t.Errorf("closed job %d still covers port 5", j.ID)
	}
	if j := Covering(jobs, 7, t0.Add(30*time.Minute)); j == nil || j.ID != 2 {
		t.Errorf("job 2 no longer covers port 7: %v", j)
	}
}
