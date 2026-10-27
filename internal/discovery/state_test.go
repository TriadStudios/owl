package discovery

import (
	"os"
	"slices"
	"testing"
	"time"
)

func TestBuildResolvesFDBToPhysicalPort(t *testing.T) {
	f, err := os.Open("../../testdata/walk-datto.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	vars, err := ParseWalk(f)
	if err != nil {
		t.Fatal(err)
	}
	state, err := Build(vars, time.Time{})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		port, ifIndex, macCount int
		mac, lldpSysName        string
	}{
		{port: 2, ifIndex: 2, macCount: 12, mac: "14:b5:cd:00:00:02", lldpSysName: "GS-BE18000"},
		{port: 3, ifIndex: 3, macCount: 1, mac: "04:92:26:00:00:01", lldpSysName: ""},
	}
	for _, c := range cases {
		p, ok := state.Ports[c.port]
		if !ok {
			t.Fatalf("port %d missing", c.port)
		}
		if p.IfIndex != c.ifIndex || !p.OperUp {
			t.Errorf("port %d: ifIndex %d up %t, want ifIndex %d up", c.port, p.IfIndex, p.OperUp, c.ifIndex)
		}
		if len(p.MACs) != c.macCount || !slices.Contains(p.MACs, c.mac) {
			t.Errorf("port %d: MACs %v, want %d including %s", c.port, p.MACs, c.macCount, c.mac)
		}
		if p.LLDP == nil || p.LLDP.SysName != c.lldpSysName {
			t.Errorf("port %d: LLDP %+v, want sysName %q", c.port, p.LLDP, c.lldpSysName)
		}
		if p.Sources["macs"] != "snmp" || p.Sources["lldp"] != "lldp" {
			t.Errorf("port %d: sources %v", c.port, p.Sources)
		}
	}
	if len(state.Ports) != 10 {
		t.Errorf("got %d ethernet ports, want 10 (LAG and VLAN interfaces excluded)", len(state.Ports))
	}
}
