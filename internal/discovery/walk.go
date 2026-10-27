package discovery

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gosnmp/gosnmp"
)

const (
	oidSysDescr              = "1.3.6.1.2.1.1.1"
	oidIfDescr               = "1.3.6.1.2.1.2.2.1.2"
	oidIfName                = "1.3.6.1.2.1.31.1.1.1.1"
	oidIfType                = "1.3.6.1.2.1.2.2.1.3"
	oidIfOperStatus          = "1.3.6.1.2.1.2.2.1.8"
	oidIfInOctets            = "1.3.6.1.2.1.2.2.1.10"
	oidIfOutOctets           = "1.3.6.1.2.1.2.2.1.16"
	oidQBridgeFDBPort        = "1.3.6.1.2.1.17.7.1.2.2.1.2"
	oidBridgeFDBPort         = "1.3.6.1.2.1.17.4.3.1.2"
	oidBasePortIfIndex       = "1.3.6.1.2.1.17.1.4.1.2"
	oidLLDPRemChassisSubtype = "1.0.8802.1.1.2.1.4.1.1.4"
	oidLLDPRemChassisID      = "1.0.8802.1.1.2.1.4.1.1.5"
	oidLLDPRemPortSubtype    = "1.0.8802.1.1.2.1.4.1.1.6"
	oidLLDPRemPortID         = "1.0.8802.1.1.2.1.4.1.1.7"
	oidLLDPRemSysName        = "1.0.8802.1.1.2.1.4.1.1.9"
)

var walkRoots = []string{
	oidSysDescr,
	oidIfDescr,
	oidIfName,
	oidIfType,
	oidIfOperStatus,
	oidIfInOctets,
	oidIfOutOctets,
	oidQBridgeFDBPort,
	oidBridgeFDBPort,
	oidBasePortIfIndex,
	oidLLDPRemChassisSubtype,
	oidLLDPRemChassisID,
	oidLLDPRemPortSubtype,
	oidLLDPRemPortID,
	oidLLDPRemSysName,
}

type Config struct {
	Host      string
	Community string
}

type Var struct {
	OID   string
	Value string
}

func Walk(cfg Config) ([]Var, error) {
	if cfg.Host == "" || cfg.Community == "" {
		return nil, fmt.Errorf("switch host and community are required")
	}
	snmp := &gosnmp.GoSNMP{
		Target:    cfg.Host,
		Port:      161,
		Community: cfg.Community,
		Version:   gosnmp.Version2c,
		Timeout:   5 * time.Second,
		Retries:   1,
		MaxOids:   gosnmp.MaxOids,
	}
	if err := snmp.Connect(); err != nil {
		return nil, fmt.Errorf("connect %s: %w", cfg.Host, err)
	}
	defer snmp.Conn.Close()

	var vars []Var
	for _, root := range walkRoots {
		pdus, err := snmp.BulkWalkAll(root)
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", root, err)
		}
		for _, pdu := range pdus {
			vars = append(vars, Var{OID: strings.TrimPrefix(pdu.Name, "."), Value: formatValue(pdu)})
		}
	}
	return vars, nil
}

func formatValue(pdu gosnmp.SnmpPDU) string {
	switch v := pdu.Value.(type) {
	case []byte:
		if isPrintable(v) {
			return strconv.Quote(string(v))
		}
		return "0x" + hex.EncodeToString(v)
	case string:
		return strings.TrimPrefix(v, ".")
	default:
		return gosnmp.ToBigInt(v).String()
	}
}

func isPrintable(b []byte) bool {
	for _, r := range string(b) {
		if r == unicode.ReplacementChar || !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

func WriteWalk(w io.Writer, vars []Var) error {
	bw := bufio.NewWriter(w)
	for _, v := range vars {
		if _, err := fmt.Fprintf(bw, "%s = %s\n", v.OID, v.Value); err != nil {
			return err
		}
	}
	return bw.Flush()
}

func ParseWalk(r io.Reader) ([]Var, error) {
	var vars []Var
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		oid, value, ok := strings.Cut(line, " = ")
		if !ok {
			return nil, fmt.Errorf("line %d: missing ' = '", n)
		}
		vars = append(vars, Var{OID: oid, Value: value})
	}
	return vars, sc.Err()
}
