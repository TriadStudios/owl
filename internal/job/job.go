package job

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Job struct {
	ID      int       `json:"id"`
	Port    int       `json:"port"`
	Tech    string    `json:"tech"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	Note    string    `json:"note,omitempty"`
	Created time.Time `json:"created"`
	Closed  time.Time `json:"closed,omitzero"`
}

func ParseWindow(s string, now time.Time) (time.Time, time.Time, error) {
	from, to, ok := strings.Cut(s, "-")
	if !ok {
		return time.Time{}, time.Time{}, fmt.Errorf("window %q: want HH:MM-HH:MM", s)
	}
	clock := func(v string) (time.Time, error) {
		t, err := time.ParseInLocation("15:04", strings.TrimSpace(v), now.Location())
		if err != nil {
			return time.Time{}, fmt.Errorf("window %q: want HH:MM-HH:MM", s)
		}
		return time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location()), nil
	}
	start, err := clock(from)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	end, err := clock(to)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if !end.After(start) {
		end = end.AddDate(0, 0, 1)
	}
	return start, end, nil
}

func Load(path string) ([]Job, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var jobs []Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return jobs, nil
}

func Save(path string, jobs []Job) error {
	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func Add(path string, j Job) (Job, error) {
	if j.Port < 1 || j.Tech == "" || !j.End.After(j.Start) {
		return Job{}, errors.New("a job needs a port, a tech and a window that ends after it starts")
	}
	jobs, err := Load(path)
	if err != nil {
		return Job{}, err
	}
	j.ID = 1
	for _, o := range jobs {
		j.ID = max(j.ID, o.ID+1)
	}
	return j, Save(path, append(jobs, j))
}

func Close(path string, id int, now time.Time) error {
	jobs, err := Load(path)
	if err != nil {
		return err
	}
	for i := range jobs {
		if jobs[i].ID != id {
			continue
		}
		if !jobs[i].Closed.IsZero() {
			return fmt.Errorf("job %d already closed", id)
		}
		jobs[i].Closed = now.UTC()
		return Save(path, jobs)
	}
	return fmt.Errorf("no job %d", id)
}

func Covering(jobs []Job, port int, now time.Time) *Job {
	for i, j := range jobs {
		if j.Port == port && j.Closed.IsZero() && !now.Before(j.Start) && !now.After(j.End) {
			return &jobs[i]
		}
	}
	return nil
}
