package narrate

import "testing"

func TestCheck(t *testing.T) {
	verdicts := map[int]string{1: "do not touch", 2: "do not touch", 3: "safe", 4: "unknown", 7: "caution"}
	cases := []struct {
		line    string
		port    int
		mustSay bool
		ok      bool
	}{
		{"Port 2, second from the left, is the uplink to GS-BE18000. Do not touch it.", 2, true, true},
		{"port 2 is do-not-touch: it's the uplink.", 2, true, true},
		{"Port 3 is safe to pull, it's the idle NAS.", 3, true, true},
		{"Port 4: unknown, the camera can't confirm the cable.", 4, true, true},
		{"Port 7 calls for caution, it's busy right now.", 7, true, true},
		{"Port 2 is unsafe, do not touch it.", 2, true, true},
		{"Port 12 is the uplink, do not touch.", 2, true, false},
		{"Port 23 is the uplink, do not touch.", 2, true, false},
		{"The second port is the uplink, do not touch.", 2, true, false},
		{"Port 2 is the uplink to GS-BE18000.", 2, true, false},
		{"Port 2 is the uplink, so it's not safe; do not touch.", 2, true, false},
		{"Port 2 is safe enough, just be careful.", 2, true, false},
		{"Port 3 is safe, but port 2 is do not touch.", 3, true, true},
		{"Port 3 is safe, but port 7 is do not touch.", 3, true, false},
		{"Port 3 is do not touch.", 3, true, false},
		{"Go ahead, wanheda will drop off for a second.", 7, false, true},
		{"Go ahead, wanheda will drop off for a second.", 7, true, false},
		{"Hold on, your hand is on port 2, that one's do not touch. Port 7 is five over to the right.", 7, false, true},
		{"Hold on, that's safe, port 2 is the uplink.", 2, false, false},
		{"Which port are you after?", 0, false, true},
		{"Do you mean the NAS on port 3?", 0, false, true},
		{"Careful, that one's do not touch.", 0, false, false},
		{"I'm not sure which, the status is unknown.", 0, false, false},
	}
	for _, c := range cases {
		err := Check(c.line, c.port, verdicts, c.mustSay)
		if (err == nil) != c.ok {
			t.Errorf("Check(%q, %d, mustSay %t) = %v, want ok %t", c.line, c.port, c.mustSay, err, c.ok)
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
