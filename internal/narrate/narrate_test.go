package narrate

import "testing"

func TestCheck(t *testing.T) {
	cases := []struct {
		line    string
		port    int
		verdict string
		ok      bool
	}{
		{"Port 2, second from the left, is the uplink to GS-BE18000. Do not touch it.", 2, "do not touch", true},
		{"port 2 is do-not-touch: it's the uplink.", 2, "do not touch", true},
		{"Port 3 is safe to pull, it's the idle NAS.", 3, "safe", true},
		{"Port 4: unknown, the camera can't confirm the cable.", 4, "unknown", true},
		{"Port 7 calls for caution, it's busy right now.", 7, "caution", true},
		{"Port 2 is unsafe, do not touch it.", 2, "do not touch", true},
		{"Port 12 is the uplink, do not touch.", 2, "do not touch", false},
		{"Port 23 is the uplink, do not touch.", 2, "do not touch", false},
		{"The second port is the uplink, do not touch.", 2, "do not touch", false},
		{"Port 2 is the uplink to GS-BE18000.", 2, "do not touch", false},
		{"Port 2 is the uplink, so it's not safe; do not touch.", 2, "do not touch", false},
		{"Port 2 is safe enough, just be careful.", 2, "do not touch", false},
		{"Port 3 is safe, but port 2 is do not touch.", 3, "safe", false},
		{"Port 3 is safe.", 3, "do-not-touch", false},
	}
	for _, c := range cases {
		err := Check(c.line, c.port, c.verdict)
		if (err == nil) != c.ok {
			t.Errorf("Check(%q, %d, %q) = %v, want ok %t", c.line, c.port, c.verdict, err, c.ok)
		}
	}
}

func TestCheckChat(t *testing.T) {
	for line, ok := range map[string]bool{
		"Which port are you after?":                     true,
		"Do you mean the NAS on port 3?":                true,
		"Sure, port 3 is safe.":                         false,
		"Careful, that one's do not touch.":             false,
		"I'm not sure which, the status is unknown.":    false,
		"Take your time, I'll keep an eye on the rack.": true,
	} {
		if err := CheckChat(line); (err == nil) != ok {
			t.Errorf("CheckChat(%q) = %v, want ok %t", line, err, ok)
		}
	}
}

func TestParse(t *testing.T) {
	r, err := parse("```json\n{\"port\": 2, \"unclear\": false, \"goodbye\": false, \"line\": \"Port 2 is do not touch.\"}\n```")
	if err != nil || r.Port != 2 || r.Line != "Port 2 is do not touch." {
		t.Fatalf("fenced reply: %+v %v", r, err)
	}
	r, err = parse(`{"port": null, "unclear": true, "goodbye": false, "line": "Which port?"}`)
	if err != nil || r.Port != 0 || !r.Unclear {
		t.Fatalf("unclear reply: %+v %v", r, err)
	}
	for _, bad := range []string{"Port 2 is do not touch.", `{"port": 0, "line": "x"}`, `{"port": 2, "line": ""}`} {
		if _, err := parse(bad); err == nil {
			t.Errorf("parse(%q) accepted", bad)
		}
	}
}
