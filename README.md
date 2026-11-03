# OWL

**Overwatch for Wiring and Links.** OWL checks a maintenance ticket against the live switch and the camera before the first cable moves, then watches the technician's hands until the job is done.

In September 2026 Google lost a zone for about four hours. A physical maintenance procedure unplugged every fiber path in 13 minutes. Nobody went rogue. A person correctly followed a procedure that was wrong, and nothing in the room told them what each cable carried. Almost every network closet has that same gap. OWL closes it for one switch and one rack face: it knows what is on every port, it checks the work order against that, and it warns before a hand pulls the wrong cable.

Built for the Meta hackathon.

## The demo

1. A ticket comes in: "take wanheda offline, port 7".
2. OWL checks it against the live switch. wanheda is actually on port 5. Port 7 is the access point, and its verdict is do not touch. The order is flagged before anyone goes near the rack.
3. The tech accepts the order on the phone, by tap, or by saying "accept work order 1042" in the glasses.
4. In the closet, Reachy Mini wakes, turns to the tech and watches the switch. The phone and the glasses show the order: "PORT 5 (not 7)".
5. The tech reaches for port 7. OWL sees the hand on the port and warns them: stop, that is the access point.
6. The tech moves to port 5 and pulls it. The switch reports the link down, and OWL says the pull is confirmed.
7. The order closes with its evidence: what OWL told the tech, what the switch confirmed, and a video clip of each event.

## How it fits together

```
   Reachy Mini camera        iPhone app "OWL Relay"         Meta Ray-Ban Display
   (fixed, on the switch)    phone camera, mic, AR tags     camera, card on the display,
          |                           |                     glasses mic
          v                           |   <-- Wearables Device Access Toolkit --
   owl-robot bridge  ---frames--->  owl-perception (Python)
   (camera, head, voice)            SAM 3.1: ports, cables, hands
          |                                   |
          +-----------------+-----------------+
                            v
                     owl (Go, the closet box)
   discovery  SNMP read, LLDP, bridge FDB, DHCP leases, NetBox
   reconcile  camera port vs switch port, with a source per fact
   blast      what is behind each port, is it live, verdict
   policy     policy/rules.yaml, deterministic
   guardian   wake, track hands, warn, confirm pulls on the switch
   narrate    Muse Spark phrases the spoken line, checked against policy
   job, ui    work-order API, relay WebSocket, findings, evidence
                            |
                            v
        work-order apps (examples/workorder), phone Orders tab
```

- **Server (`cmd/owl`, `internal/*`)**, Go. Polls the switch over SNMP, joins it with LLDP neighbors, the bridge forwarding table and DHCP leases, and works out what each port carries, whether it is live and what would break. Serves an HTTPS API and a WebSocket for the phone.
- **Perception (`perception/`)**, Python. Segments the switch face with SAM 3.1 using the prompts `ethernet port`, `network port` and `ethernet cable`, numbers the ports left to right, reads link LEDs, and tracks the hand to tell which port it is on. Rejects any frame whose port count does not match the switch.
- **Reachy Mini.** The fixed camera on the rack. Motion wakes it, it turns to the tech, talks, and listens. `owl-robot` owns its camera stream and serves frames to perception.
- **iPhone app (`relay/ios`, "OWL Relay")**. Streams phone or glasses frames to the server, draws port tags over the live view, speaks OWL's lines, listens hands-free, and has an Orders tab to accept and finish work orders.
- **Meta glasses.** Through the Meta Wearables Device Access Toolkit the app uses the glasses camera as its frame source, shows a card on the Ray-Ban Display (the work order, the port under the hand and its verdict, OWL's last line), and takes speech from the glasses mic.
- **Work-order API.** Any ticketing tool can file an order. OWL finds the device on the live switch, flags a wrong port, and records what happened while the order was open.

## Safety design

- **Read-only against the switch.** OWL uses SNMP GET and walks only. No SNMP SET, no web portal automation, no config writes. A pull is confirmed by watching the port's oper status, never by changing it.
- **Verdicts come from code, not from a model.** `internal/blast` and `internal/policy` decide safe, caution, do not touch or unknown from the switch facts and `policy/rules.yaml` (liveness window, traffic threshold, what counts as an uplink, critical names). `owl policy lint` checks the file.
- **The language model only phrases.** The narrator gets the port table with its verdicts and returns a sentence. `narrate.Check` rejects any line where a verdict word does not match policy for the port it follows, or that talks about a different port. A rejected or late line falls back to a fixed template. An empty line means stay quiet.
- **The client never holds switch credentials.** The phone, the glasses and API clients only send frames and words, and only receive overlays, cards and lines.
- **Pinned TLS and per-device tokens.** `owl serve` makes a self-signed certificate. `owl pair` gives each device its own token and the certificate's SHA-256 fingerprint, and clients pin that fingerprint. The server stores only a hash of each token. `owl pair revoke` cuts a device off on a running server.
- **Unknown stays unknown.** Every fact on screen carries its source (`snmp`, `lldp`, `arp`, `dhcp`, `netbox`, `camera`, `human`). A port OWL cannot place is shown as unknown, not guessed.

## Meta technology used

- **SAM 3.1** through the Meta API (`api.meta.ai`, model `sam-3.1`) segments ports, cables and hands. Responses are cached on disk by image and prompt, and a daily cap guards spend. A local backend runs `facebook/sam3` on the GPU through Hugging Face Transformers.
- **Muse Spark** through the Meta API (`api.meta.ai/v1/responses`, model `muse-spark-1.3`) phrases what Reachy, the phone and the glasses say. It never decides a verdict.
- **Meta Wearables Device Access Toolkit** (iOS): `MWDATCore` and `MWDATCamera` for the glasses camera stream, `MWDATDisplay` for the card on the Ray-Ban Display, and `MWDATSpeech` for on-glasses transcription.

Voice on the robot uses ElevenLabs (speech and Scribe for speech to text).

## Build on OWL

OWL exposes work orders over its HTTPS API. Every call carries `Authorization: Bearer <token>` and pins the server's certificate.

| Call | What it does |
| - | - |
| `POST /api/workorders` | File an order with `ticket`, `device`, `claimed_port`, `tech`, `note`, `minutes`. OWL returns the real `port`, `mismatch` and `check`, its finding in words. |
| `GET /api/workorders` | All orders with their status: open, assigned, accepted, closed or expired. |
| `GET /api/workorders/{id}` | One order with the device's port now and its events, with clips under `/api/evidence/{hash}`. |
| `POST /api/workorders/{id}/accept` | A tech takes the order. |
| `POST /api/workorders/{id}/close` | Close it. |

`examples/workorder` is a small standalone Go app that files orders, shows OWL's finding and shows the evidence. See [examples/workorder/README.md](examples/workorder/README.md).

Other read endpoints: `/api/health`, `/api/ports`, `/api/blast/{port}`, `/api/findings`.

## Running it

### What you need

- Go 1.26 and `uv` with Python 3.11 or newer.
- A managed L2 switch with SNMP v2c read access and LLDP on. OWL was built against a Datto DSW250-8P-2X (8 copper ports).
- A DHCP client list exported from the router, as CSV.
- A Reachy Mini, or another camera that can see the switch face.
- A Meta API key for SAM 3.1 and Muse Spark. An ElevenLabs key for the robot's voice.
- Optional: an NVIDIA GPU for local SAM 3, Docker for NetBox (`deploy/netbox/docker-compose.yml`).
- For the phone app: a Mac with Xcode, an iPhone, and Meta glasses registered with the Wearables Device Access Toolkit. Open `relay/ios/OWLRelay.xcodeproj` and set your own signing team.

### Configuration

Put settings in `.env.local` at the repo root (gitignored). Both the Go server and the Python services read it.

| Variable | Used for |
| - | - |
| `OWL_SWITCH_HOST`, `OWL_SWITCH_COMMUNITY` | The switch to poll (read-only community). |
| `OWL_DHCP_LEASES` | Path to the router's DHCP client list CSV. |
| `OWL_META_API_KEY` | SAM 3.1 and Muse Spark. Without it, narration uses templates only. |
| `OWL_SAM_BACKEND` | `meta-api` (default) or `local`. |
| `OWL_SAM_DAILY_CAP` | Most SAM images billed per day. |
| `OWL_REACHY_HOST` | The Reachy Mini on the network. |
| `ELEVENLABS_API_KEY`, `OWL_ELEVENLABS_VOICE` | Robot voice and speech to text. |
| `OWL_NETBOX_URL`, `OWL_NETBOX_TOKEN` | Optional NetBox source of truth. |

### Commands

```bash
go build -o bin/owl ./cmd/owl

# Robot bridge first: it owns the camera stream. Then perception.
(cd perception && uv run owl-robot)        # http://127.0.0.1:8702
(cd perception && uv run owl-perception)   # http://127.0.0.1:8701

# Check the switch and the camera agree.
./bin/owl snmp walk
./bin/owl reconcile -image frame.png
./bin/owl blast -port 5 -image frame.png
./bin/owl policy lint

# Pin each cable to a port (LED and traffic correlation, touch to confirm) and save a baseline.
./bin/owl enroll

# The guardian: Reachy wakes, watches hands, warns and confirms pulls.
./bin/owl watch

# The API server for the phone, the glasses and work-order apps (HTTPS on :8080).
./bin/owl serve
./bin/owl pair -name phone          # QR code for the iPhone app
./bin/owl pair list
./bin/owl pair revoke phone

# The sample work-order app on http://127.0.0.1:8090.
./bin/owl pair -name workorder -json data/workorder-pairing.json
go run ./examples/workorder

# After the tech leaves: anything moved without an open job is a finding.
./bin/owl diff -image after.png
```

Useful flags: `-rules` (policy file), `-perception` and `-robot` (service URLs), `-leases`, `-addr` (serve), `-url` (pair). `owl bench -expect` runs repeated captures against a known occupancy pattern, such as `X.X.X.X.` for ports 1, 3, 5 and 7 plugged.

`make check` runs `gofmt`, `go vet`, the Go tests, `owl policy lint` and the perception tests on fixtures in `testdata/`.

## Results

Measured on one Datto 8-port switch in a home closet, Oct 28 – Nov 2, 2026. Phone figures are for the Nov 2 build, after the crop-reuse speedup.

| What | Fixed robot camera | Handheld phone |
| - | - | - |
| Port reading correct, on frames OWL accepted | 88/88 frames, 704/704 port readings (0 wrong in 511 accepted frames across all runs) | — |
| Frames rejected as unreadable (OWL says "no reading" rather than guess) | 2/90 (2%) | 11/36 (31%) |
| Cables matched to ports at enrollment (camera + switch evidence) | 7/8 verified, 1 unknown, 0 wrong | — |
| Hand on the right port (replayed recordings) | 12/13 frames | 5/5 touches |
| Hand tracking rate | ~5.9 fps | one reading per 2–4 s |
| Time per frame | — | median 3.8 s (n=25) |
| Spoken request → verdict line ready | median 2.1 s (n=28) | median 2.0 s narration (n=9) |
| Stop decision → alert starts playing | ~80 ms | — |
| Hand on a port → verdict line ready | 2.7–5.1 s (n=4) | 4.9–5.4 s (n=2) |
| Wrong-cable "Stop" warnings in unscripted walk-ins | 7 fired: 2 confirmed right, 1 false, 1 missed, 4 unscored | — |
| Verdict lines phrased by the model (rest fall back to a fixed template) | 29/32 | 9/9 |
| Wrong ticket port caught by the work-order check | 1/1 | 1/1 |

- Small samples from one switch and one person. The walk-ins were debugging sessions with fixes between them, not a frozen-build trial.
- The verdict itself always comes from deterministic policy. The language model only phrases it, and a check replaces any line that names a different port or verdict.
- Drift detection precision/recall and a staged wrong-pull test (10 wrong + 10 correct reaches) have not been run yet.

## Limits and what's next

- One switch model and one rack face. The SNMP mapping is generic L2, but only the Datto has been tested. No patch panels, fiber or multi-switch recursion yet.
- The fixed camera needs a measured port region for its rack face. Move the camera and the region has to be set again. Handheld phone frames place the row per frame, which is slower.
- Hand-to-port is geometry on the SAM hand mask. It works well on a single reach, but a pinch that straddles two ports, or a hand at the end of the row, can still land on the wrong neighbor.
- LEDs that stay dark on a live port can't be used for enrollment; those ports fall back to touch or stay unknown.
- The glasses display shows cards, not overlays pinned to the cables. The toolkit's display API draws panels, not world-anchored graphics. The phone draws the AR tags.
- Speech to text can mishear port numbers ("port five" as "part one"). OWL answers about the port it heard and says the number, so the tech can catch it.
- SAM 3.1 through the API takes seconds per frame and costs a call per prompt. A local model on a bigger GPU would cut both.
- Next: more switch vendors over SNMP and their REST APIs, cable tag OCR, patch panels, and drift reports pushed back into ticketing.
