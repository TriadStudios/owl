package ui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/triadstudios/owl/internal/finding"
	"github.com/triadstudios/owl/internal/job"
	"github.com/triadstudios/owl/internal/narrate"
	"github.com/triadstudios/owl/internal/reconcile"
)

const defaultOrderMinutes = 60

type workOrderIn struct {
	Ticket  string `json:"ticket"`
	Device  string `json:"device"`
	Claimed int    `json:"claimed_port"`
	Tech    string `json:"tech"`
	Note    string `json:"note"`
	Minutes int    `json:"minutes"`
}

type workOrderJSON struct {
	job.Job
	Status   string `json:"status"`
	Mismatch bool   `json:"mismatch"`
}

type workOrderDetail struct {
	workOrderJSON
	DevicePort   int               `json:"device_port_now,omitempty"`
	DeviceOnline bool              `json:"device_online_now"`
	Events       []finding.Finding `json:"events"`
}

func newWorkOrder(j job.Job, now time.Time) workOrderJSON {
	status := "open"
	switch {
	case !j.Closed.IsZero():
		status = "closed"
	case now.After(j.End):
		status = "expired"
	}
	return workOrderJSON{Job: j, Status: status, Mismatch: j.Claimed != 0 && j.Claimed != j.Port}
}

// locate finds the port a device hangs off by hostname, IP or MAC. A device can also be counted behind an
// uplink, so the port with the fewest devices on it is the one it is plugged into.
func locate(device string, ports []reconcile.Port) (int, bool) {
	want := strings.ToLower(strings.TrimSpace(device))
	best, fewest, online := 0, 0, false
	for _, p := range ports {
		if p.Blast == nil {
			continue
		}
		for _, d := range p.Blast.Devices {
			host, _, _ := strings.Cut(strings.ToLower(d.Hostname), ".")
			if want == "" || (want != host && want != strings.ToLower(d.Hostname) && want != d.IP && want != strings.ToLower(d.MAC)) {
				continue
			}
			if best == 0 || len(p.Blast.Devices) < fewest {
				best, fewest, online = p.Num, len(p.Blast.Devices), d.Online
			}
		}
	}
	return best, online
}

func describe(num int, ports []reconcile.Port) string {
	for _, p := range ports {
		if p.Num != num {
			continue
		}
		c := newCard(p.Blast)
		what := c.Downstream
		if what == "" {
			what = "nothing known"
		}
		return fmt.Sprintf("port %d is %s, %s", num, what, strings.ReplaceAll(c.Verdict, "-", " "))
	}
	return fmt.Sprintf("port %d is not on the switch", num)
}

// check is decided from the live switch alone, so a ticket written from an old spreadsheet is caught before anyone reaches for a cable.
func check(device string, claimed, found int, ports []reconcile.Port) string {
	switch {
	case device == "":
		return fmt.Sprintf("The ticket names port %d and no device; %s.", claimed, describe(claimed, ports))
	case found == 0:
		return fmt.Sprintf("OWL can't find %s on the switch; %s.", device, describe(claimed, ports))
	case claimed != 0 && claimed != found:
		return fmt.Sprintf("The ticket says port %d for %s, but the switch sees %s on port %d. Don't touch port %d: %s.", claimed, device, device, found, claimed, describe(claimed, ports))
	default:
		return fmt.Sprintf("%s is on port %d; %s.", device, found, describe(found, ports))
	}
}

func (s *Server) createWorkOrder(w http.ResponseWriter, r *http.Request) {
	var in workOrderIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		s.fail(w, http.StatusBadRequest, "body must be a JSON work order")
		return
	}
	in.Device, in.Tech = strings.TrimSpace(in.Device), strings.TrimSpace(in.Tech)
	if in.Tech == "" || (in.Device == "" && in.Claimed < 1) {
		s.fail(w, http.StatusBadRequest, "a work order needs a tech and a device or a port")
		return
	}
	s.mu.Lock()
	ports := s.ports
	s.mu.Unlock()
	if ports == nil {
		s.fail(w, http.StatusServiceUnavailable, errNoPoll.Error())
		return
	}
	found := 0
	if in.Device != "" {
		found, _ = locate(in.Device, ports)
	}
	target := found
	if target == 0 {
		target = in.Claimed
	}
	if target == 0 {
		s.fail(w, http.StatusUnprocessableEntity, fmt.Sprintf("OWL can't find %s on the switch and the ticket names no port", in.Device))
		return
	}
	minutes := in.Minutes
	if minutes <= 0 {
		minutes = defaultOrderMinutes
	}
	now := time.Now()
	s.jobsMu.Lock()
	j, err := job.Add(s.JobsPath, job.Job{
		Port: target, Tech: in.Tech, Start: now, End: now.Add(time.Duration(minutes) * time.Minute), Note: in.Note, Created: now.UTC(),
		Ticket: in.Ticket, Device: in.Device, Claimed: in.Claimed, Check: check(in.Device, in.Claimed, found, ports),
	})
	s.jobsMu.Unlock()
	if err != nil {
		s.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	s.Log.Info("work order", "id", j.ID, "ticket", j.Ticket, "device", j.Device, "claimed", j.Claimed, "port", j.Port, "check", j.Check)
	s.writeJSON(w, http.StatusCreated, newWorkOrder(j, now))
}

func (s *Server) listWorkOrders(w http.ResponseWriter, r *http.Request) {
	jobs, err := job.Load(s.JobsPath)
	if err != nil {
		s.Log.Error("jobs", "err", err)
		s.fail(w, http.StatusInternalServerError, "cannot read work orders")
		return
	}
	now := time.Now()
	out := []workOrderJSON{}
	for i := len(jobs) - 1; i >= 0; i-- {
		out = append(out, newWorkOrder(jobs[i], now))
	}
	s.writeJSON(w, http.StatusOK, map[string][]workOrderJSON{"work_orders": out})
}

func (s *Server) workOrder(w http.ResponseWriter, r *http.Request) {
	j, ok := s.findJob(w, r)
	if !ok {
		return
	}
	found, err := finding.Load(s.FindingsPath)
	if err != nil {
		s.Log.Error("findings", "err", err)
		s.fail(w, http.StatusInternalServerError, "cannot read findings")
		return
	}
	until := j.End
	if !j.Closed.IsZero() {
		until = j.Closed
	}
	out := workOrderDetail{workOrderJSON: newWorkOrder(j, time.Now()), Events: []finding.Finding{}}
	for _, f := range found {
		if f.JobID == j.ID || f.JobID == 0 && !f.TS.Before(j.Start) && !f.TS.After(until) {
			out.Events = append(out.Events, f)
		}
	}
	if j.Device != "" {
		s.mu.Lock()
		ports := s.ports
		s.mu.Unlock()
		out.DevicePort, out.DeviceOnline = locate(j.Device, ports)
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) closeWorkOrder(w http.ResponseWriter, r *http.Request) {
	j, ok := s.findJob(w, r)
	if !ok {
		return
	}
	s.jobsMu.Lock()
	err := job.Close(s.JobsPath, j.ID, time.Now())
	s.jobsMu.Unlock()
	if err != nil {
		s.fail(w, http.StatusConflict, err.Error())
		return
	}
	s.Log.Info("work order closed", "id", j.ID, "ticket", j.Ticket)
	s.workOrder(w, r)
}

func (s *Server) findJob(w http.ResponseWriter, r *http.Request) (job.Job, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		s.fail(w, http.StatusBadRequest, "work order id must be an integer")
		return job.Job{}, false
	}
	jobs, err := job.Load(s.JobsPath)
	if err != nil {
		s.Log.Error("jobs", "err", err)
		s.fail(w, http.StatusInternalServerError, "cannot read work orders")
		return job.Job{}, false
	}
	for _, j := range jobs {
		if j.ID == id {
			return j, true
		}
	}
	s.fail(w, http.StatusNotFound, fmt.Sprintf("no work order %d", id))
	return job.Job{}, false
}

func (s *Server) openOrders(now time.Time) []job.Job {
	jobs, err := job.Load(s.JobsPath)
	if err != nil {
		s.Log.Error("jobs", "err", err)
		return nil
	}
	var open []job.Job
	for _, j := range jobs {
		if j.Closed.IsZero() && !now.Before(j.Start) && !now.After(j.End) {
			open = append(open, j)
		}
	}
	return open
}

func narrateJobs(open []job.Job) []narrate.Job {
	var out []narrate.Job
	for _, j := range open {
		out = append(out, narrate.Job{ID: j.ID, Port: j.Port, Tech: j.Tech, Note: j.Note, Until: j.End.Local().Format("15:04"), Ticket: j.Ticket, Device: j.Device, Claimed: j.Claimed, Check: j.Check})
	}
	return out
}

// witness keeps what OWL told the tech during an open work order, so the order closes with its own transcript.
func (s *Server) witness(port int, heard, said string) {
	now := time.Now()
	open := s.openOrders(now)
	if len(open) == 0 {
		return
	}
	f := finding.Finding{Type: "warning", Port: port, TS: now.UTC(), JobID: open[0].ID, Heard: heard, Text: said}
	if j := job.Covering(open, port, now); j != nil {
		f.Type, f.JobID = "guidance", j.ID
	}
	if err := finding.Append(s.FindingsPath, []finding.Finding{f}); err != nil {
		s.Log.Error("work order transcript", "err", err)
	}
}
