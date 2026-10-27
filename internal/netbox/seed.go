package netbox

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/triadstudios/owl/internal/discovery"
)

const (
	SiteSlug   = "closet"
	CopperPort = 8
	SFPPorts   = 2
)

type SeedInput struct {
	Switch   string
	Clients  []discovery.Device
	Uplink   []string
	Critical []string
}

func Seed(c *Client, in SeedInput) (int, error) {
	for _, t := range []struct{ slug, color string }{{"uplink", "2196f3"}, {"critical", "f44336"}, {"backup-target", "ff9800"}} {
		if _, err := c.Ensure("extras/tags/", url.Values{"slug": {t.slug}}, map[string]any{"name": t.slug, "slug": t.slug, "color": t.color}); err != nil {
			return 0, err
		}
	}
	for _, f := range []struct{ name, kind string }{{"owl_status", "text"}, {"owl_evidence", "text"}, {"owl_verified_at", "datetime"}} {
		if _, err := c.Ensure("extras/custom-fields/", url.Values{"name": {f.name}}, map[string]any{"name": f.name, "type": f.kind, "object_types": []string{"dcim.interface", "dcim.cable"}}); err != nil {
			return 0, err
		}
	}

	site, err := c.Ensure("dcim/sites/", url.Values{"slug": {SiteSlug}}, map[string]any{"name": SiteSlug, "slug": SiteSlug, "status": "active"})
	if err != nil {
		return 0, err
	}
	rack, err := c.Ensure("dcim/racks/", url.Values{"site_id": {strconv.Itoa(site)}, "name": {"closet-rack"}}, map[string]any{"name": "closet-rack", "site": site, "status": "active"})
	if err != nil {
		return 0, err
	}
	ids := map[string]int{}
	for _, e := range []struct{ path, slug, name string }{
		{"dcim/manufacturers/", "datto", "Datto"},
		{"dcim/manufacturers/", "generic", "Generic"},
		{"dcim/device-roles/", "switch", "switch"},
		{"dcim/device-roles/", "client", "client"},
	} {
		if ids[e.slug], err = c.Ensure(e.path, url.Values{"slug": {e.slug}}, map[string]any{"name": e.name, "slug": e.slug}); err != nil {
			return 0, err
		}
	}
	switchType, err := c.Ensure("dcim/device-types/", url.Values{"slug": {"dsw250-8p-2x"}}, map[string]any{"manufacturer": ids["datto"], "model": "DSW250-8P-2X", "slug": "dsw250-8p-2x", "u_height": 1})
	if err != nil {
		return 0, err
	}
	clientType, err := c.Ensure("dcim/device-types/", url.Values{"slug": {"client"}}, map[string]any{"manufacturer": ids["generic"], "model": "client", "slug": "client", "u_height": 0})
	if err != nil {
		return 0, err
	}

	sw, err := c.Ensure("dcim/devices/", url.Values{"site_id": {strconv.Itoa(site)}, "name": {in.Switch}}, map[string]any{
		"name": in.Switch, "device_type": switchType, "role": ids["switch"], "site": site, "rack": rack, "position": 1, "face": "front", "status": "active",
	})
	if err != nil {
		return 0, err
	}
	for n := 1; n <= CopperPort+SFPPorts; n++ {
		kind := "1000base-t"
		if n > CopperPort {
			kind = "10gbase-x-sfpp"
		}
		name := fmt.Sprintf("Slot0/%d", n)
		if _, err := c.Ensure("dcim/interfaces/", url.Values{"device_id": {strconv.Itoa(sw)}, "name": {name}}, map[string]any{"device": sw, "name": name, "type": kind}); err != nil {
			return 0, err
		}
	}

	names := map[string]int{}
	for _, d := range in.Clients {
		names[strings.ToLower(clientName(d))]++
	}
	created := 0
	for _, d := range in.Clients {
		name := clientName(d)
		if strings.EqualFold(name, in.Switch) {
			continue
		}
		if names[strings.ToLower(name)] > 1 {
			name += "-" + strings.ReplaceAll(d.MAC[12:], ":", "")
		}
		tags := []map[string]string{}
		for slug, list := range map[string][]string{"uplink": in.Uplink, "critical": in.Critical} {
			if slices.ContainsFunc(list, func(n string) bool { return strings.EqualFold(n, d.Hostname) }) {
				tags = append(tags, map[string]string{"slug": slug})
			}
		}
		device, err := c.Ensure("dcim/devices/", url.Values{"site_id": {strconv.Itoa(site)}, "name": {name}}, map[string]any{
			"name": name, "device_type": clientType, "role": ids["client"], "site": site, "status": "active", "tags": tags,
		})
		if err != nil {
			return 0, err
		}
		iface, err := c.Ensure("dcim/interfaces/", url.Values{"device_id": {strconv.Itoa(device)}, "name": {"eth0"}}, map[string]any{"device": device, "name": "eth0", "type": "other"})
		if err != nil {
			return 0, err
		}
		mac, err := c.Ensure("dcim/mac-addresses/", url.Values{"mac_address": {d.MAC}}, map[string]any{"mac_address": d.MAC, "assigned_object_type": "dcim.interface", "assigned_object_id": iface})
		if err != nil {
			return 0, err
		}
		if err := c.Do("PATCH", fmt.Sprintf("dcim/interfaces/%d/", iface), nil, map[string]any{"primary_mac_address": mac}, nil); err != nil {
			return 0, err
		}
		created++
	}
	return created, nil
}

func clientName(d discovery.Device) string {
	if d.Hostname != "" {
		return d.Hostname
	}
	return "client-" + strings.ReplaceAll(d.MAC[9:], ":", "")
}
