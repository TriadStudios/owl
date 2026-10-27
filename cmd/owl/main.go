package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/triadstudios/owl/internal/discovery"
)

const usage = "usage: owl snmp walk|dump [-host H] [-community C] [-out FILE]"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "owl:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 2 || args[0] != "snmp" {
		return errors.New(usage)
	}
	if err := loadEnvFile(".env.local"); err != nil {
		return err
	}
	fset := flag.NewFlagSet("owl snmp "+args[1], flag.ContinueOnError)
	cfg := discovery.Config{}
	fset.StringVar(&cfg.Host, "host", os.Getenv("OWL_SWITCH_HOST"), "switch address")
	fset.StringVar(&cfg.Community, "community", "", "SNMP v2c community (default $OWL_SWITCH_COMMUNITY)")
	out := fset.String("out", "testdata/walk-datto.txt", "walk output file")
	if err := fset.Parse(args[2:]); err != nil {
		return err
	}
	if cfg.Community == "" {
		cfg.Community = os.Getenv("OWL_SWITCH_COMMUNITY")
	}

	switch args[1] {
	case "walk":
		vars, err := discovery.Walk(cfg)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
			return err
		}
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		if err := discovery.WriteWalk(f, vars); err != nil {
			f.Close()
			return fmt.Errorf("write %s: %w", *out, err)
		}
		if err := f.Close(); err != nil {
			return err
		}
		fmt.Printf("%d vars -> %s\n", len(vars), *out)
		return nil
	case "dump":
		state, err := discovery.Poll(cfg)
		if err != nil {
			return err
		}
		nums := make([]int, 0, len(state.Ports))
		for n := range state.Ports {
			nums = append(nums, n)
		}
		sort.Ints(nums)
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "PORT\tIFINDEX\tUP\tIN OCTETS\tOUT OCTETS\tMACS\tLLDP NEIGHBOR")
		for _, n := range nums {
			p := state.Ports[n]
			neighbor := "-"
			if p.LLDP != nil {
				neighbor = fmt.Sprintf("%s (%s, port %s)", p.LLDP.SysName, p.LLDP.ChassisID, p.LLDP.PortID)
			}
			fmt.Fprintf(tw, "%d\t%d\t%t\t%d\t%d\t%d\t%s\n", p.Num, p.IfIndex, p.OperUp, p.InOctets, p.OutOctets, len(p.MACs), neighbor)
		}
		return tw.Flush()
	default:
		return errors.New(usage)
	}
}

func loadEnvFile(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if _, set := os.LookupEnv(key); !set {
			os.Setenv(key, strings.Trim(strings.TrimSpace(value), `"'`))
		}
	}
	return sc.Err()
}
