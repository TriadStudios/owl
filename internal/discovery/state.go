package discovery

import (
	"encoding/hex"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	ifTypeEthernetCsmacd  = 6
	lldpChassisSubtypeMAC = "4"
	lldpPortSubtypeMAC    = "3"
)

type SwitchState struct {
	Polled time.Time
	Ports  map[int]*PortState
}

type PortState struct {
	Num       int
	IfIndex   int
	OperUp    bool
	InOctets  uint64
	OutOctets uint64
	MACs      []string
	LLDP      *Neighbor
	Sources   map[string]string
}

type Neighbor struct {
	SysName, ChassisID, PortID string
	Capabilities               []string
}

var LLDPCapabilities = []string{"other", "repeater", "bridge", "wlan-access-point", "router", "telephone", "docsis", "station", "c-vlan", "s-vlan", "two-port-mac-relay"}

var trailingNumber = regexp.MustCompile(`(\d+)$`)

func Poll(cfg Config) (*SwitchState, error) {
	vars, err := Walk(cfg)
	if err != nil {
		return nil, err
	}
	return Build(vars, time.Now())
}

func Build(vars []Var, polled time.Time) (*SwitchState, error) {
	tables := map[string]map[string]string{}
	for _, v := range vars {
		for _, root := range walkRoots {
			if suffix, ok := strings.CutPrefix(v.OID, root+"."); ok {
				if tables[root] == nil {
					tables[root] = map[string]string{}
				}
				tables[root][suffix] = v.Value
				break
			}
		}
	}

	state := &SwitchState{Polled: polled, Ports: map[int]*PortState{}}
	byIfIndex := map[int]*PortState{}
	// ifDescr is empty on the Datto DSW250; ifName carries "Slot0/N".
	for idx, raw := range tables[oidIfName] {
		ifIndex, err := strconv.Atoi(idx)
		if err != nil {
			return nil, fmt.Errorf("ifName index %q: %w", idx, err)
		}
		ifType, err := strconv.Atoi(tables[oidIfType][idx])
		if err != nil {
			return nil, fmt.Errorf("ifType for ifIndex %d: %w", ifIndex, err)
		}
		if ifType != ifTypeEthernetCsmacd {
			continue
		}
		name, err := decodeText(raw)
		if err != nil {
			return nil, fmt.Errorf("ifName for ifIndex %d: %w", ifIndex, err)
		}
		m := trailingNumber.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		num, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, fmt.Errorf("port number in %q: %w", name, err)
		}
		if prev, dup := state.Ports[num]; dup {
			return nil, fmt.Errorf("port %d claimed by ifIndex %d and %d", num, prev.IfIndex, ifIndex)
		}
		p := &PortState{Num: num, IfIndex: ifIndex, Sources: map[string]string{"num": "snmp", "ifIndex": "snmp"}}
		if s, ok := tables[oidIfOperStatus][idx]; ok {
			p.OperUp = s == "1"
			p.Sources["operUp"] = "snmp"
		}
		if s, ok := tables[oidIfInOctets][idx]; ok {
			if p.InOctets, err = strconv.ParseUint(s, 10, 64); err != nil {
				return nil, fmt.Errorf("ifInOctets for ifIndex %d: %w", ifIndex, err)
			}
			p.Sources["inOctets"] = "snmp"
		}
		if s, ok := tables[oidIfOutOctets][idx]; ok {
			if p.OutOctets, err = strconv.ParseUint(s, 10, 64); err != nil {
				return nil, fmt.Errorf("ifOutOctets for ifIndex %d: %w", ifIndex, err)
			}
			p.Sources["outOctets"] = "snmp"
		}
		state.Ports[num] = p
		byIfIndex[ifIndex] = p
	}

	byBridgePort := map[int]*PortState{}
	for bp, raw := range tables[oidBasePortIfIndex] {
		bridgePort, err := strconv.Atoi(bp)
		if err != nil {
			return nil, fmt.Errorf("dot1dBasePort %q: %w", bp, err)
		}
		ifIndex, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("dot1dBasePortIfIndex for bridge port %d: %w", bridgePort, err)
		}
		if p, ok := byIfIndex[ifIndex]; ok {
			byBridgePort[bridgePort] = p
		}
	}

	fdb := tables[oidQBridgeFDBPort]
	if len(fdb) == 0 {
		fdb = tables[oidBridgeFDBPort]
	}
	for suffix, raw := range fdb {
		bridgePort, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("FDB port for %s: %w", suffix, err)
		}
		p, ok := byBridgePort[bridgePort]
		if !ok {
			continue
		}
		mac, err := macFromOIDSuffix(suffix)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(p.MACs, mac) {
			p.MACs = append(p.MACs, mac)
			p.Sources["macs"] = "snmp"
		}
	}

	// lldpRemLocalPortNum equals dot1dBasePort on 802.1D/Q bridges per LLDP-MIB.
	for suffix, raw := range tables[oidLLDPRemSysName] {
		parts := strings.Split(suffix, ".")
		if len(parts) != 3 {
			return nil, fmt.Errorf("LLDP index %q: want timeMark.localPort.remIndex", suffix)
		}
		localPort, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, fmt.Errorf("LLDP local port in %q: %w", suffix, err)
		}
		p, ok := byBridgePort[localPort]
		if !ok {
			continue
		}
		var n Neighbor
		if n.SysName, err = decodeText(raw); err != nil {
			return nil, fmt.Errorf("LLDP sysName %s: %w", suffix, err)
		}
		if n.PortID, err = decodeID(tables[oidLLDPRemPortID][suffix], tables[oidLLDPRemPortSubtype][suffix] == lldpPortSubtypeMAC); err != nil {
			return nil, fmt.Errorf("LLDP port ID %s: %w", suffix, err)
		}
		if n.ChassisID, err = decodeID(tables[oidLLDPRemChassisID][suffix], tables[oidLLDPRemChassisSubtype][suffix] == lldpChassisSubtypeMAC); err != nil {
			return nil, fmt.Errorf("LLDP chassis ID %s: %w", suffix, err)
		}
		if raw, ok := tables[oidLLDPRemSysCapEnabled][suffix]; ok {
			bits, err := decodeText(raw)
			if err != nil {
				return nil, fmt.Errorf("LLDP capabilities %s: %w", suffix, err)
			}
			for i, name := range LLDPCapabilities {
				if i/8 < len(bits) && bits[i/8]&(0x80>>(i%8)) != 0 {
					n.Capabilities = append(n.Capabilities, name)
				}
			}
		}
		p.LLDP = &n
		p.Sources["lldp"] = "lldp"
	}
	return state, nil
}

func decodeText(raw string) (string, error) {
	if strings.HasPrefix(raw, "0x") {
		b, err := hex.DecodeString(raw[2:])
		return string(b), err
	}
	return strconv.Unquote(raw)
}

func decodeID(raw string, isMAC bool) (string, error) {
	if raw == "" {
		return "", nil
	}
	s, err := decodeText(raw)
	if err != nil || !isMAC {
		return s, err
	}
	if len(s) != 6 {
		return "", fmt.Errorf("MAC subtype with %d bytes", len(s))
	}
	return net.HardwareAddr(s).String(), nil
}

func macFromOIDSuffix(suffix string) (string, error) {
	parts := strings.Split(suffix, ".")
	if len(parts) < 6 {
		return "", fmt.Errorf("FDB index %q: too short for a MAC", suffix)
	}
	b := make([]byte, 6)
	for i, s := range parts[len(parts)-6:] {
		n, err := strconv.ParseUint(s, 10, 8)
		if err != nil {
			return "", fmt.Errorf("FDB index %q: %w", suffix, err)
		}
		b[i] = byte(n)
	}
	return net.HardwareAddr(b).String(), nil
}
