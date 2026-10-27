package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/triadstudios/owl/internal/blast"
	"github.com/triadstudios/owl/internal/discovery"
	"github.com/triadstudios/owl/internal/perception"
	"github.com/triadstudios/owl/internal/policy"
	"github.com/triadstudios/owl/internal/reconcile"
)

const usage = "usage: owl snmp walk|dump [-host H] [-community C] [-out FILE] | owl reconcile -image FILE [-leases CSV] [-perception URL] [-rules FILE] | owl blast -port N [-leases CSV] [-rules FILE]"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "owl:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	var cmd string
	switch {
	case len(args) >= 2 && args[0] == "snmp":
		cmd, args = "snmp "+args[1], args[2:]
	case len(args) >= 1 && (args[0] == "reconcile" || args[0] == "blast"):
		cmd, args = args[0], args[1:]
	default:
		return errors.New(usage)
	}
	if err := loadEnvFile(".env.local"); err != nil {
		return err
	}
	fset := flag.NewFlagSet("owl "+cmd, flag.ContinueOnError)
	cfg := discovery.Config{}
	fset.StringVar(&cfg.Host, "host", os.Getenv("OWL_SWITCH_HOST"), "switch address")
	fset.StringVar(&cfg.Community, "community", "", "SNMP v2c community (default $OWL_SWITCH_COMMUNITY)")
	out := fset.String("out", "testdata/walk-datto.txt", "walk output file")
	image := fset.String("image", "", "switch face frame")
	leases := fset.String("leases", os.Getenv("OWL_DHCP_LEASES"), "DHCP client list CSV")
	perceptionURL := fset.String("perception", "http://127.0.0.1:8701", "perception service URL")
	rulesPath := fset.String("rules", "policy/rules.yaml", "policy rules file")
	port := fset.Int("port", 0, "physical port number")
	if err := fset.Parse(args); err != nil {
		return err
	}
	if cfg.Community == "" {
		cfg.Community = os.Getenv("OWL_SWITCH_COMMUNITY")
	}

	switch cmd {
	case "snmp walk":
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
	case "snmp dump":
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
	case "reconcile":
		if *image == "" {
			return errors.New("reconcile needs -image")
		}
		frame, err := os.ReadFile(*image)
		if err != nil {
			return err
		}
		client := perception.Client{BaseURL: *perceptionURL, HTTP: &http.Client{Timeout: 6 * time.Minute}}
		cam, err := client.Segment(context.Background(), frame, http.DetectContentType(frame))
		if err != nil {
			return err
		}
		ids, rules, err := loadPolicyInputs(*leases, *rulesPath)
		if err != nil {
			return err
		}
		prev, cur, err := pollTwice(cfg, rules.LivenessWindow)
		if err != nil {
			return err
		}
		ports, err := reconcile.Join(cam, prev, cur, ids, rules)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "PORT\tCAMERA\tSWITCH\tSTATUS\tVERDICT\tDEVICE(S)\tSOURCES")
		for _, p := range ports {
			var verdict, downstream string
			if p.Blast != nil {
				verdict, downstream = string(p.Blast.Verdict), p.Blast.Downstream
			}
			cells := []string{p.Camera, p.Switch, string(p.Status), verdict, downstream, strings.Join(p.Sources, ",")}
			for i, c := range cells {
				if c == "" {
					cells[i] = "-"
				}
			}
			fmt.Fprintf(tw, "%d\t%s\n", p.Num, strings.Join(cells, "\t"))
		}
		return tw.Flush()
	case "blast":
		if *port <= 0 {
			return errors.New("blast needs -port N")
		}
		ids, rules, err := loadPolicyInputs(*leases, *rulesPath)
		if err != nil {
			return err
		}
		prev, cur, err := pollTwice(cfg, rules.LivenessWindow)
		if err != nil {
			return err
		}
		card, err := blast.Assess(*port, prev, cur, ids, rules)
		if err != nil {
			return err
		}
		fmt.Printf("port %d: %s\n", card.Port, strings.ToUpper(string(card.Verdict)))
		fmt.Printf("  downstream   %s\n", card.Downstream)
		fmt.Printf("  behind       %d MAC(s), LLDP neighbor %q\n", card.Behind, card.Neighbor)
		fmt.Printf("  liveness     %s (%d B in %s)\n", card.Liveness, card.DeltaBytes, card.Window.Round(time.Second))
		fmt.Printf("  criticality  %s\n", strings.Join(card.Criticality, ", "))
		fmt.Println("  devices")
		for _, d := range card.Devices {
			fmt.Printf("    %s  %-15s  %-20s  %s  [%s]\n", d.MAC, d.IP, d.Hostname, d.Vendor, strings.Join(d.Sources, ","))
		}
		fmt.Println("  inputs")
		for _, in := range card.Inputs {
			fmt.Printf("    - %s\n", in)
		}
		return nil
	default:
		return errors.New(usage)
	}
}

func loadPolicyInputs(leasesPath, rulesPath string) (*discovery.Identities, policy.Rules, error) {
	if leasesPath == "" {
		return nil, policy.Rules{}, errors.New("need -leases or OWL_DHCP_LEASES")
	}
	lf, err := os.Open(leasesPath)
	if err != nil {
		return nil, policy.Rules{}, err
	}
	defer lf.Close()
	ids, err := discovery.LoadIdentities(lf)
	if err != nil {
		return nil, policy.Rules{}, fmt.Errorf("%s: %w", leasesPath, err)
	}
	rf, err := os.Open(rulesPath)
	if err != nil {
		return nil, policy.Rules{}, err
	}
	defer rf.Close()
	rules, err := policy.Load(rf)
	if err != nil {
		return nil, policy.Rules{}, fmt.Errorf("%s: %w", rulesPath, err)
	}
	return ids, rules, nil
}

func pollTwice(cfg discovery.Config, window time.Duration) (*discovery.SwitchState, *discovery.SwitchState, error) {
	prev, err := discovery.Poll(cfg)
	if err != nil {
		return nil, nil, err
	}
	time.Sleep(window)
	cur, err := discovery.Poll(cfg)
	if err != nil {
		return nil, nil, err
	}
	return prev, cur, nil
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
