package finding

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

type Finding struct {
	Type        string    `json:"type"`
	Port        int       `json:"port"`
	TS          time.Time `json:"ts"`
	BeforeFrame string    `json:"before_frame,omitempty"`
	AfterFrame  string    `json:"after_frame,omitempty"`
	Diff        string    `json:"diff,omitempty"`
	JobID       int       `json:"job_id,omitempty"`
}

func Load(path string) ([]Finding, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var findings []Finding
	if err := json.Unmarshal(data, &findings); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return findings, nil
}

func Append(path string, add []Finding) error {
	findings, err := Load(path)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(append(findings, add...), "", "  ")
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
