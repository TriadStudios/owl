# Work orders on OWL

A sample ticketing app that shows how to build on OWL. It files a work order, shows what OWL found when it
checked the ticket against the live switch, and shows the evidence OWL kept while the work was done.

```bash
owl pair -name workorder -json data/workorder-pairing.json
go run ./examples/workorder
```

Then open http://127.0.0.1:8090. The pairing file holds a token, so keep it out of git (`data/` is ignored).

## The API

Every call carries `Authorization: Bearer <token>` and goes to OWL over TLS, pinned to the
`cert_sha256` from pairing (see `pinned` in `main.go`).

| Call | What it does |
| - | - |
| `POST /api/workorders` | File an order: `{"ticket", "device", "claimed_port", "tech", "note", "minutes"}`. OWL finds the device on the live switch and returns the order with `port` (where the device really is), `mismatch` and `check`, its finding in words. |
| `GET /api/workorders` | All orders, newest first, with `status` (open, closed or expired). |
| `GET /api/workorders/{id}` | One order with `device_port_now`, `device_online_now` and `events`: what OWL told the tech and what the switch confirmed, with clips under `/api/evidence/{hash}`. |
| `POST /api/workorders/{id}/close` | Close it. |

While an order is open, the robot, the phone and the glasses all know its device and its real port, so a tech
who follows a wrong ticket is steered to the right cable before anything is pulled.
