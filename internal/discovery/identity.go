package discovery

import (
	_ "embed"
	"encoding/csv"
	"fmt"
	"io"
	"net"
	"strings"
)

//go:embed oui.csv
var ouiCSV string

const unknownVendor = "Loading manufacturer.."

type Device struct {
	MAC, IP, Hostname, Vendor string
	Online                    bool
	Sources                   []string
}

type Identities struct {
	leases map[string]Device
	oui    map[string]string
}

func LoadIdentities(leases io.Reader) (*Identities, error) {
	ids := &Identities{leases: map[string]Device{}, oui: map[string]string{}}

	ouiRows, err := csv.NewReader(strings.NewReader(ouiCSV)).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("oui.csv: %w", err)
	}
	for _, row := range ouiRows[1:] {
		ids.oui[row[0]] = row[1]
	}

	r := csv.NewReader(leases)
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("lease header: %w", err)
	}
	col := map[string]int{}
	for i, name := range header {
		col[strings.TrimPrefix(strings.TrimSpace(name), "\ufeff")] = i
	}
	for _, name := range []string{"Client Name", "Client IP address", "Clients MAC Address", "Device Type"} {
		if _, ok := col[name]; !ok {
			return nil, fmt.Errorf("lease file has no %q column", name)
		}
	}
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("lease row: %w", err)
		}
		hw, err := net.ParseMAC(strings.TrimSpace(row[col["Clients MAC Address"]]))
		if err != nil {
			return nil, fmt.Errorf("lease row: %w", err)
		}
		d := Device{MAC: hw.String(), Sources: []string{"dhcp"}}
		name := strings.TrimSpace(row[col["Client Name"]])
		if _, err := net.ParseMAC(name); err != nil {
			d.Hostname = name
		}
		if ip := strings.TrimSpace(row[col["Client IP address"]]); !strings.EqualFold(ip, "offline") {
			if net.ParseIP(ip) == nil {
				return nil, fmt.Errorf("lease %s: bad IP %q", d.MAC, ip)
			}
			d.IP, d.Online = ip, true
		}
		if v := strings.TrimSpace(row[col["Device Type"]]); v != "" && v != unknownVendor {
			d.Vendor = v
		}
		ids.leases[d.MAC] = d
	}
	return ids, nil
}

func (ids *Identities) Lookup(mac string) Device {
	d, ok := ids.leases[mac]
	if !ok {
		d = Device{MAC: mac}
	}
	if d.Vendor == "" {
		if v, ok := ids.oui[strings.ToUpper(strings.ReplaceAll(mac[:8], ":", ""))]; ok {
			d.Vendor = v
			d.Sources = append(d.Sources, "oui")
		}
	}
	return d
}
