package ui

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	seenEvery      = time.Minute
	pairCodeMaxAge = 10 * time.Minute
)

type Device struct {
	Name        string    `json:"name"`
	TokenSHA256 string    `json:"token_sha256"`
	Created     time.Time `json:"created"`
	LastSeen    time.Time `json:"last_seen,omitzero"`
}

var deviceName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

func LoadDevices(path string) ([]Device, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var devices []Device
	if err := json.Unmarshal(data, &devices); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return devices, nil
}

func SaveDevices(path string, devices []Device) error {
	data, err := json.MarshalIndent(devices, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func Pair(path, name string, now time.Time) (string, error) {
	if !deviceName.MatchString(name) {
		return "", fmt.Errorf("device name %q: use 1-32 of A-Z a-z 0-9 . _ -", name)
	}
	devices, err := LoadDevices(path)
	if err != nil {
		return "", err
	}
	if slices.ContainsFunc(devices, func(d Device) bool { return d.Name == name }) {
		return "", fmt.Errorf("device %q is already paired; revoke it first or pick another -name", name)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	devices = append(devices, Device{Name: name, TokenSHA256: tokenHash(token), Created: now.UTC()})
	return token, SaveDevices(path, devices)
}

func Revoke(path, name string) error {
	devices, err := LoadDevices(path)
	if err != nil {
		return err
	}
	kept := slices.DeleteFunc(devices, func(d Device) bool { return d.Name == name })
	if len(kept) == len(devices) {
		return fmt.Errorf("no paired device %q", name)
	}
	return SaveDevices(path, kept)
}

func PairCodePath(devicesPath, name string) string {
	return filepath.Join(filepath.Dir(devicesPath), "pair-"+name+".png")
}

func StalePairCodes(devicesPath string, now time.Time) ([]string, error) {
	devices, err := LoadDevices(devicesPath)
	if err != nil {
		return nil, err
	}
	codes, err := filepath.Glob(PairCodePath(devicesPath, "*"))
	if err != nil {
		return nil, err
	}
	var stale []string
	for _, code := range codes {
		info, err := os.Stat(code)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(code), "pair-"), ".png")
		used := slices.ContainsFunc(devices, func(d Device) bool { return d.Name == name && !d.LastSeen.IsZero() })
		if !used && now.Sub(info.ModTime()) > pairCodeMaxAge {
			stale = append(stale, code)
		}
	}
	return stale, nil
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Registry rereads devices.json whenever it changes on disk, so `owl pair` and `owl pair revoke` take effect on a running server.
type Registry struct {
	Path string

	mu      sync.Mutex
	devices []Device
	modTime time.Time
	size    int64
	loaded  bool
}

func (r *Registry) Authenticate(token string, now time.Time) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.refresh(); err != nil {
		return "", err
	}
	want := []byte(tokenHash(token))
	name, idx := "", -1
	for i, d := range r.devices {
		if subtle.ConstantTimeCompare(want, []byte(d.TokenSHA256)) == 1 {
			name, idx = d.Name, i
		}
	}
	if idx < 0 || now.Sub(r.devices[idx].LastSeen) < seenEvery {
		return name, nil
	}
	r.loaded = false
	if err := r.refresh(); err != nil {
		return "", err
	}
	i := slices.IndexFunc(r.devices, func(d Device) bool { return d.Name == name })
	if i < 0 {
		return "", nil
	}
	first := r.devices[i].LastSeen.IsZero()
	r.devices[i].LastSeen = now.UTC()
	if err := SaveDevices(r.Path, r.devices); err != nil {
		return "", err
	}
	r.loaded = false
	if first {
		if err := os.Remove(PairCodePath(r.Path, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return name, fmt.Errorf("delete pairing QR: %w", err)
		}
	}
	return name, nil
}

func (r *Registry) refresh() error {
	info, err := os.Stat(r.Path)
	if errors.Is(err, fs.ErrNotExist) {
		r.devices, r.loaded = nil, true
		return nil
	}
	if err != nil {
		return err
	}
	if r.loaded && info.ModTime().Equal(r.modTime) && info.Size() == r.size {
		return nil
	}
	devices, err := LoadDevices(r.Path)
	if err != nil {
		return err
	}
	r.devices, r.modTime, r.size, r.loaded = devices, info.ModTime(), info.Size(), true
	return nil
}
