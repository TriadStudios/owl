package policy

import (
	"bufio"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/triadstudios/owl/internal/discovery"
)

type Rules struct {
	LivenessWindow      time.Duration
	LivenessActiveBytes uint64
	UplinkCapabilities  []string
	CriticalNames       []string
}

func Load(r io.Reader) (Rules, error) {
	values := map[string]string{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return Rules{}, fmt.Errorf("rules line %d: want key: value", n)
		}
		key = strings.TrimSpace(key)
		if _, dup := values[key]; dup {
			return Rules{}, fmt.Errorf("rules line %d: %s set twice", n, key)
		}
		values[key] = strings.TrimSpace(value)
	}
	if err := sc.Err(); err != nil {
		return Rules{}, err
	}

	var rules Rules
	for key, value := range values {
		var err error
		switch key {
		case "liveness_window_seconds":
			var s uint64
			if s, err = strconv.ParseUint(value, 10, 32); err == nil && s == 0 {
				err = fmt.Errorf("must be positive")
			}
			rules.LivenessWindow = time.Duration(s) * time.Second
		case "liveness_active_bytes":
			rules.LivenessActiveBytes, err = strconv.ParseUint(value, 10, 64)
		case "uplink_capabilities":
			if rules.UplinkCapabilities, err = parseList(value); err == nil {
				for _, c := range rules.UplinkCapabilities {
					if !slices.Contains(discovery.LLDPCapabilities, c) {
						err = fmt.Errorf("unknown LLDP capability %q", c)
					}
				}
			}
		case "critical_names":
			if rules.CriticalNames, err = parseList(value); err == nil {
				for _, p := range rules.CriticalNames {
					if _, matchErr := path.Match(p, ""); matchErr != nil {
						err = fmt.Errorf("bad pattern %q", p)
					}
				}
			}
		default:
			err = fmt.Errorf("unknown key")
		}
		if err != nil {
			return Rules{}, fmt.Errorf("rules %s: %w", key, err)
		}
	}
	for _, key := range []string{"liveness_window_seconds", "liveness_active_bytes", "uplink_capabilities", "critical_names"} {
		if _, ok := values[key]; !ok {
			return Rules{}, fmt.Errorf("rules: %s missing", key)
		}
	}
	return rules, nil
}

func parseList(value string) ([]string, error) {
	inner, open := strings.CutPrefix(value, "[")
	inner, closed := strings.CutSuffix(inner, "]")
	if !open || !closed {
		return nil, fmt.Errorf("want a one-line [list], got %q", value)
	}
	var items []string
	for _, item := range strings.Split(inner, ",") {
		if item = strings.Trim(strings.TrimSpace(item), `"'`); item != "" {
			items = append(items, strings.ToLower(item))
		}
	}
	return items, nil
}
