package netbox

import (
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type termination struct {
	ObjectID int `json:"object_id"`
	Object   struct {
		Name   string `json:"name"`
		Device Ref    `json:"device"`
	} `json:"object"`
}

type cable struct {
	ID     int           `json:"id"`
	A      []termination `json:"a_terminations"`
	B      []termination `json:"b_terminations"`
	Status struct {
		Value string `json:"value"`
	} `json:"status"`
}

func (c *Client) SwitchInterface(device string, num int) (*Interface, error) {
	var ifaces []Interface
	name := fmt.Sprintf("Slot0/%d", num)
	if err := c.List("dcim/interfaces/", url.Values{"device": {device}, "name": {name}}, &ifaces); err != nil {
		return nil, err
	}
	if len(ifaces) != 1 {
		return nil, fmt.Errorf("netbox: %d interfaces named %s on %s; run owl netbox seed", len(ifaces), name, device)
	}
	return &ifaces[0], nil
}

func (c *Client) RecordPort(iface int, status, evidence string, verifiedAt time.Time) error {
	var at any
	if !verifiedAt.IsZero() {
		at = verifiedAt.UTC().Format(time.RFC3339)
	}
	return c.Do(http.MethodPatch, fmt.Sprintf("dcim/interfaces/%d/", iface), nil, map[string]any{
		"custom_fields": map[string]any{"owl_status": status, "owl_evidence": evidence, "owl_verified_at": at},
	}, nil)
}

func (c *Client) Connect(a, b *Interface) error {
	for _, end := range []*Interface{a, b} {
		if end.Cable == nil {
			continue
		}
		var existing cable
		if err := c.Do(http.MethodGet, fmt.Sprintf("dcim/cables/%d/", end.Cable.ID), nil, nil, &existing); err != nil {
			return err
		}
		if len(existing.A) == 1 && len(existing.B) == 1 &&
			(existing.A[0].ObjectID == a.ID && existing.B[0].ObjectID == b.ID || existing.A[0].ObjectID == b.ID && existing.B[0].ObjectID == a.ID) {
			return nil
		}
		if err := c.Do(http.MethodDelete, fmt.Sprintf("dcim/cables/%d/", existing.ID), nil, nil, nil); err != nil {
			return err
		}
	}
	return c.Do(http.MethodPost, "dcim/cables/", nil, map[string]any{
		"a_terminations": []map[string]any{{"object_type": "dcim.interface", "object_id": a.ID}},
		"b_terminations": []map[string]any{{"object_type": "dcim.interface", "object_id": b.ID}},
		"status":         "connected",
	}, nil)
}

func (c *Client) Cables(device string) ([]string, error) {
	var cables []cable
	if err := c.List("dcim/cables/", url.Values{"device": {device}}, &cables); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(cables))
	for _, cb := range cables {
		if len(cb.A) != 1 || len(cb.B) != 1 {
			return nil, fmt.Errorf("netbox: cable %d has %d+%d terminations", cb.ID, len(cb.A), len(cb.B))
		}
		out = append(out, fmt.Sprintf("#%d  %s %s <-> %s %s  (%s)", cb.ID, cb.A[0].Object.Device.Name, cb.A[0].Object.Name, cb.B[0].Object.Device.Name, cb.B[0].Object.Name, cb.Status.Value))
	}
	return out, nil
}
