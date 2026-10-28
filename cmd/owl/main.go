package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/triadstudios/owl/internal/blast"
	"github.com/triadstudios/owl/internal/discovery"
	"github.com/triadstudios/owl/internal/enroll"
	"github.com/triadstudios/owl/internal/netbox"
	"github.com/triadstudios/owl/internal/perception"
	"github.com/triadstudios/owl/internal/policy"
	"github.com/triadstudios/owl/internal/reconcile"
)

const (
	passiveWindows  = 3
	pulseOn         = 2 * time.Second
	pulseOff        = 2 * time.Second
	pulseCycles     = 10
	pulseSample     = time.Second
	maxPingTargets  = 4
	pulseRate       = 256 << 10
	pingPayload     = 64000
	enrollBurst     = 10 * time.Second
	touchMinChange  = 12.0
	touchDominance  = 1.5
	touchStripDepth = 1.0
	baselinePath    = "data/baseline.json"
)

const usage = "usage: owl snmp walk|dump [-host H] [-community C] [-out FILE] | owl reconcile -image FILE [-leases CSV] [-perception URL] [-rules FILE] | owl blast -port N -image FILE [-leases CSV] [-rules FILE] | owl enroll | owl netbox seed [-leases CSV] [-uplink NAMES] [-critical NAMES] | owl policy lint [-rules FILE]"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "owl:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	var cmd string
	switch {
	case len(args) >= 2 && (args[0] == "snmp" || args[0] == "netbox" || args[0] == "policy"):
		cmd, args = args[0]+" "+args[1], args[2:]
	case len(args) >= 1 && (args[0] == "reconcile" || args[0] == "blast" || args[0] == "enroll"):
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
	switchName := fset.String("switch", "DSW250-8P-2X", "switch device name in NetBox")
	uplink := fset.String("uplink", "GS-BE18000", "comma-separated client names to tag uplink")
	critical := fset.String("critical", "closet-box", "comma-separated client names to tag critical")
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
		in, err := buildInputs(cfg, *image, *perceptionURL, *leases, *rulesPath)
		if err != nil {
			return err
		}
		ports, err := reconcile.Join(in)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "PORT\tCAMERA\tSWITCH\tSTATUS\tVERDICT\tDEVICE(S)\tSOURCES")
		for _, p := range ports {
			var verdict, downstream string
			if p.Blast != nil {
				verdict, downstream = string(p.Blast.Verdict), p.Blast.Downstream
				if p.Blast.Unknown != "" {
					verdict = "unknown (" + p.Blast.Unknown + ")"
				}
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
			return errors.New("blast needs -port N and -image")
		}
		in, err := buildInputs(cfg, *image, *perceptionURL, *leases, *rulesPath)
		if err != nil {
			return err
		}
		card, err := blast.Assess(*port, in)
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
	case "enroll":
		nb, err := newNetbox()
		if err != nil {
			return err
		}
		cam := perception.Client{BaseURL: *perceptionURL, HTTP: &http.Client{Timeout: 6 * time.Minute}}
		ctx := context.Background()
		frame, err := cam.SegmentCamera(ctx, time.Second)
		if err != nil {
			return err
		}
		boxes := make([][4]float64, len(frame.Ports))
		for i, c := range frame.Ports {
			boxes[i] = [4]float64{c.Box[0], frame.Row[0], c.Box[2], frame.Row[1]}
		}
		var windows []enroll.Window
		for range passiveWindows {
			prev, err := discovery.Poll(cfg)
			if err != nil {
				return err
			}
			leds, err := cam.LEDs(ctx, boxes, enrollBurst)
			if err != nil {
				return err
			}
			cur, err := discovery.Poll(cfg)
			if err != nil {
				return err
			}
			windows = append(windows, enroll.Window{LEDs: leds, Prev: prev, Cur: cur})
		}
		ports, err := enroll.Best(frame, windows, time.Now())
		if err != nil {
			return err
		}
		cur := windows[len(windows)-1].Cur

		rf, err := os.Open(*rulesPath)
		if err != nil {
			return err
		}
		rules, err := policy.Load(rf)
		rf.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", *rulesPath, err)
		}
		local := map[string]bool{}
		ifaces, err := net.Interfaces()
		if err != nil {
			return err
		}
		for _, ifc := range ifaces {
			if len(ifc.HardwareAddr) == 6 {
				local[ifc.HardwareAddr.String()] = true
			}
		}
		self := 0
		notes := map[int]string{}
		for i := range ports {
			p := &ports[i]
			sp := cur.Ports[p.Num]
			if sp == nil {
				continue
			}
			if i := slices.IndexFunc(sp.MACs, func(m string) bool { return local[m] }); i >= 0 {
				self = p.Num
				notes[p.Num] += fmt.Sprintf("; the closet box's own port: its MAC %s is in the FDB here (snmp fdb)", sp.MACs[i])
			}
			if sp.LLDP != nil {
				if i := slices.IndexFunc(sp.LLDP.Capabilities, func(c string) bool { return slices.Contains(rules.UplinkCapabilities, c) }); i >= 0 {
					// Mesh APs advertise router too; an AP is aggregation, not the uplink.
					if slices.Contains(sp.LLDP.Capabilities, "wlan-access-point") {
						notes[p.Num] += fmt.Sprintf("; not the uplink: LLDP neighbor %s advertises %s but is also a wlan-access-point (lldp)", sp.LLDP.SysName, sp.LLDP.Capabilities[i])
					} else {
						notes[p.Num] += fmt.Sprintf("; uplink: LLDP neighbor %s advertises %s (lldp)", sp.LLDP.SysName, sp.LLDP.Capabilities[i])
					}
				}
			}
		}
		var ids *discovery.Identities
		if *leases != "" {
			lf, err := os.Open(*leases)
			if err != nil {
				return err
			}
			ids, err = discovery.LoadIdentities(lf)
			lf.Close()
			if err != nil {
				return fmt.Errorf("%s: %w", *leases, err)
			}
		}
		var lit []int
		ifIndex := map[int]int{}
		for _, p := range ports {
			if p.Occupied && p.OperUp && p.LinkOn && p.Num != self {
				lit = append(lit, p.Num)
			}
			if sp := cur.Ports[p.Num]; sp != nil {
				ifIndex[p.Num] = sp.IfIndex
			}
		}
		// The router hands out the leases and has none itself, but the closet box's ARP table knows its address.
		arp := map[string]string{}
		arpCmd := exec.Command("arp", "-a")
		if runtime.GOOS == "linux" {
			arpCmd = exec.Command("ip", "neigh")
		}
		if out, err := arpCmd.Output(); err == nil {
			ipv4, mac := regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}\b`), regexp.MustCompile(`(?i)\b[0-9a-f]{2}([-:][0-9a-f]{2}){5}\b`)
			for _, line := range strings.Split(string(out), "\n") {
				if ip, m := ipv4.FindString(line), mac.FindString(line); ip != "" && m != "" {
					arp[strings.ToLower(strings.ReplaceAll(m, "-", ":"))] = ip
				}
			}
		}
		for i := range ports {
			p := &ports[i]
			if p.Status == enroll.Verified || !p.Occupied || p.Num == self || !slices.Contains(lit, p.Num) || ids == nil {
				continue
			}
			// With an LLDP neighbor, ping only it so Wi-Fi clients behind an AP or router are not flooded.
			macs, missing := p.MACs, "; no known IP to ping"
			if sp := cur.Ports[p.Num]; sp.LLDP != nil {
				macs, missing = []string{sp.LLDP.PortID, sp.LLDP.ChassisID}, fmt.Sprintf("; LLDP neighbor %s has no known IP to ping", sp.LLDP.SysName)
			}
			var ips, from []string
			for _, mac := range macs {
				ip, source := "", ""
				if d := ids.Lookup(mac); d.Online {
					ip, source = d.IP, "lease"
				} else if a, ok := arp[mac]; ok {
					ip, source = a, "arp"
				}
				if ip != "" && !slices.Contains(ips, ip) && len(ips) < maxPingTargets {
					ips, from = append(ips, ip), append(from, source)
				}
			}
			if len(ips) == 0 {
				p.Reason += missing
				continue
			}
			pingers := max(1, pulseRate/pingPayload/len(ips))
			fmt.Printf("pinging port %d (%d target, IP from %s), %d KiB/s for %s on / %s off x%d ...\n", p.Num, len(ips), strings.Join(from, ","), pingers*len(ips)*pingPayload>>10, pulseOn, pulseOff, pulseCycles)
			first, err := discovery.Counters(cfg, ifIndex)
			if err != nil {
				return err
			}
			pulse := enroll.Pulse{Start: time.Now(), On: pulseOn, Off: pulseOff, Cycles: pulseCycles, Samples: []discovery.Sample{first}}
			end := pulse.Start.Add(time.Duration(pulseCycles) * (pulseOn + pulseOff))
			sampled := make(chan error, 1)
			go func() {
				for next := pulse.Start.Add(pulseSample); !next.After(end.Add(pulseSample)); next = next.Add(pulseSample) {
					time.Sleep(time.Until(next))
					s, err := discovery.Counters(cfg, ifIndex)
					if err != nil {
						sampled <- err
						return
					}
					pulse.Samples = append(pulse.Samples, s)
				}
				sampled <- nil
			}()
			var pingErr error
			for c := range pulseCycles {
				on := pulse.Start.Add(time.Duration(c) * (pulseOn + pulseOff))
				time.Sleep(time.Until(on))
				phase, cancel := context.WithDeadline(ctx, on.Add(pulseOn))
				var wg sync.WaitGroup
				var mu sync.Mutex
				for _, ip := range ips {
					for range pingers {
						wg.Add(1)
						go func() {
							defer wg.Done()
							count := strconv.Itoa(int(pulseOn.Seconds()))
							size := strconv.Itoa(pingPayload)
							args := []string{"-c", count, "-s", size, "-W", "1", ip}
							if runtime.GOOS == "windows" {
								args = []string{"-n", count, "-l", size, "-w", "1000", ip}
							}
							err := exec.CommandContext(phase, "ping", args...).Run()
							var exit *exec.ExitError
							if err != nil && !errors.As(err, &exit) && phase.Err() == nil {
								mu.Lock()
								pingErr = err
								mu.Unlock()
							}
						}()
					}
				}
				wg.Wait()
				cancel()
			}
			if err := <-sampled; err != nil {
				return err
			}
			if pingErr != nil {
				return fmt.Errorf("ping: %w", pingErr)
			}
			if err := os.MkdirAll("data/debug", 0o755); err != nil {
				return err
			}
			dump, err := json.MarshalIndent(pulse, "", " ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join("data/debug", pulse.Start.Format("20060102T150405")+"-pulse.json"), dump, 0o644); err != nil {
				return err
			}
			others := slices.DeleteFunc(slices.Clone(lit), func(n int) bool { return n == p.Num })
			ok, why := enroll.Pulsed(pulse, p.Num, others)
			if ok {
				p.Status, p.Source, p.VerifiedAt, p.Reason = enroll.Verified, "camera+snmp+ping", time.Now(), why
			} else {
				p.Reason += "; " + why
			}
			// Every echo and reply also crosses the closet box's own port, so the same pulse must show up there and nowhere else.
			if s := slices.IndexFunc(ports, func(q enroll.Port) bool { return q.Num == self }); s >= 0 && ports[s].Occupied && ports[s].Status != enroll.Verified {
				sp := &ports[s]
				selfOK, selfWhy := enroll.Pulsed(pulse, self, others)
				if selfOK {
					sp.Status, sp.Source, sp.VerifiedAt, sp.Reason = enroll.Verified, "camera+snmp+ping", time.Now(), fmt.Sprintf("carries the closet box's ping to port %d: %s", p.Num, selfWhy)
				} else {
					sp.Reason += fmt.Sprintf("; the closet box's ping to port %d: %s", p.Num, selfWhy)
				}
			}
		}

		var regions [][4]int
		regionOf := map[int]int{}
		// Cable outlines cross below the row, so each port gets only the strip under its own plug, split at the gaps between neighbours.
		for i, c := range frame.Ports {
			if !c.Occupied {
				continue
			}
			x0, x1 := c.Box[0], c.Box[2]
			if i > 0 {
				x0 = max(x0, (frame.Ports[i-1].Box[2]+c.Box[0])/2)
			}
			if i < len(frame.Ports)-1 {
				x1 = min(x1, (c.Box[2]+frame.Ports[i+1].Box[0])/2)
			}
			y0 := frame.Row[1]
			regionOf[c.Num] = len(regions)
			regions = append(regions, [4]int{int(x0), int(y0), int(x1), int(y0+touchStripDepth*(c.Box[2]-c.Box[0])) + 1})
		}
		stdin := bufio.NewReader(os.Stdin)
		human := true
		for i := range ports {
			p := &ports[i]
			r, cabled := regionOf[p.Num]
			if p.Status == enroll.Verified || !cabled {
				continue
			}
			if !human {
				p.Reason += "; no human at the terminal"
				continue
			}
			if err := cam.MotionStart(ctx, regions); err != nil {
				return err
			}
			fmt.Printf("touch the cable in port %d, then press Enter (s + Enter skips): ", p.Num)
			line, readErr := stdin.ReadString('\n')
			changes, err := cam.MotionStop(ctx, len(regions))
			if err != nil {
				return err
			}
			if readErr != nil {
				human = false
				p.Reason += "; no human at the terminal"
				fmt.Println()
				continue
			}
			if strings.TrimSpace(line) == "s" {
				p.Reason += "; human skipped"
				continue
			}
			var other float64
			for j, ch := range changes {
				if j != r {
					other = max(other, ch)
				}
			}
			if changes[r] >= touchMinChange && changes[r] >= touchDominance*other {
				p.Status, p.Source, p.VerifiedAt = enroll.Verified, "human", time.Now()
				p.Reason = fmt.Sprintf("touch changed the port %d cable region by %.1f (largest elsewhere %.1f)", p.Num, changes[r], other)
			} else {
				p.Reason += fmt.Sprintf("; touch not confirmed (region change %.1f, largest elsewhere %.1f)", changes[r], other)
			}
		}

		for i := range ports {
			ports[i].Reason += notes[ports[i].Num]
		}
		for i := range ports {
			p := &ports[i]
			iface, err := nb.SwitchInterface(*switchName, p.Num)
			if err != nil {
				return err
			}
			if err := nb.RecordPort(iface.ID, string(p.Status), frame.FrameHash, p.VerifiedAt); err != nil {
				return err
			}
			if p.Status == enroll.Unknown && iface.Cable != nil {
				if err := nb.MarkCable(iface.Cable.ID, string(enroll.Unknown)); err != nil {
					return err
				}
			}
			if !p.Occupied || p.Status == enroll.Unknown {
				continue
			}
			var candidates []string
			if sp := cur.Ports[p.Num]; sp.LLDP != nil {
				candidates = append(candidates, sp.LLDP.PortID, sp.LLDP.ChassisID)
			}
			if len(p.MACs) == 1 {
				candidates = append(candidates, p.MACs[0])
			}
			for _, mac := range candidates {
				if _, err := net.ParseMAC(mac); err != nil {
					continue
				}
				far, err := nb.InterfaceByMAC(mac)
				if err != nil {
					return err
				}
				if far == nil {
					continue
				}
				if err := nb.Connect(iface, far, string(p.Status)); err != nil {
					return err
				}
				p.CableTo = far.Device.Name + " " + far.Name
				break
			}
		}

		baseline := enroll.Baseline{Switch: *switchName, FrameHash: frame.FrameHash, EnrolledAt: time.Now().UTC(), Ports: ports}
		data, err := json.MarshalIndent(baseline, "", "  ")
		if err != nil {
			return err
		}
		if err := os.MkdirAll("data", 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(baselinePath, append(data, '\n'), 0o644); err != nil {
			return err
		}

		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "PORT\tCAMERA\tSWITCH\tLED\tENERGY\tDELTA B\tSTATUS\tSOURCE\tCABLE TO\tREASON")
		for _, p := range ports {
			camera, sw, led, source, cableTo := "-", "down", "off", p.Source, p.CableTo
			switch {
			case p.Occupied:
				camera = "occupied"
			case p.OnCamera:
				camera = "empty"
			}
			if p.OperUp {
				sw = "up"
			}
			if p.LinkOn {
				led = "on"
			} else if !p.OnCamera {
				led = "-"
			}
			if source == "" {
				source = "-"
			}
			if cableTo == "" {
				cableTo = "-"
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%.2f\t%d\t%s\t%s\t%s\t%s\n", p.Num, camera, sw, led, p.Energy, p.DeltaBytes, p.Status, source, cableTo, p.Reason)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		fmt.Printf("\nframe %s, baseline %s\n\nnetbox cables on %s:\n", frame.FrameHash[:16], baselinePath, *switchName)
		cables, err := nb.Cables(*switchName)
		if err != nil {
			return err
		}
		for _, c := range cables {
			fmt.Println("  " + c)
		}
		return nil
	case "netbox seed":
		nb, err := newNetbox()
		if err != nil {
			return err
		}
		if *leases == "" {
			return errors.New("need -leases or OWL_DHCP_LEASES")
		}
		lf, err := os.Open(*leases)
		if err != nil {
			return err
		}
		defer lf.Close()
		ids, err := discovery.LoadIdentities(lf)
		if err != nil {
			return fmt.Errorf("%s: %w", *leases, err)
		}
		n, err := netbox.Seed(nb, netbox.SeedInput{Switch: *switchName, Clients: ids.Leased(), Uplink: strings.Split(*uplink, ","), Critical: strings.Split(*critical, ",")})
		if err != nil {
			return err
		}
		fmt.Printf("seeded %s with %d interfaces and %d clients from %s\n", *switchName, netbox.CopperPort+netbox.SFPPorts, n, *leases)
		return nil
	case "policy lint":
		rf, err := os.Open(*rulesPath)
		if err != nil {
			return err
		}
		defer rf.Close()
		if _, err := policy.Load(rf); err != nil {
			return fmt.Errorf("%s: %w", *rulesPath, err)
		}
		fmt.Printf("%s ok\n", *rulesPath)
		return nil
	default:
		return errors.New(usage)
	}
}

func newNetbox() (*netbox.Client, error) {
	base, token := os.Getenv("OWL_NETBOX_URL"), os.Getenv("OWL_NETBOX_TOKEN")
	if base == "" || token == "" {
		return nil, errors.New("OWL_NETBOX_URL and OWL_NETBOX_TOKEN are required")
	}
	return &netbox.Client{BaseURL: strings.TrimSuffix(base, "/"), Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}}, nil
}

func buildInputs(cfg discovery.Config, image, perceptionURL, leasesPath, rulesPath string) (blast.Inputs, error) {
	if image == "" || leasesPath == "" {
		return blast.Inputs{}, errors.New("need -image and -leases (or OWL_DHCP_LEASES)")
	}
	in := blast.Inputs{AttributionMin: 0.6, AggregationMin: 3}
	if v := os.Getenv("OWL_ATTRIBUTION_MIN"); v != "" {
		min, err := strconv.ParseFloat(v, 64)
		if err != nil || min < 0 || min > 1 {
			return blast.Inputs{}, fmt.Errorf("OWL_ATTRIBUTION_MIN %q: want a number in [0, 1]", v)
		}
		in.AttributionMin = min
	}
	if v := os.Getenv("OWL_AGGREGATION_MIN"); v != "" {
		min, err := strconv.Atoi(v)
		if err != nil || min < 1 {
			return blast.Inputs{}, fmt.Errorf("OWL_AGGREGATION_MIN %q: want a positive integer", v)
		}
		in.AggregationMin = min
	}
	frame, err := os.ReadFile(image)
	if err != nil {
		return blast.Inputs{}, err
	}
	client := perception.Client{BaseURL: perceptionURL, HTTP: &http.Client{Timeout: 6 * time.Minute}}
	if in.Camera, err = client.Segment(context.Background(), frame, http.DetectContentType(frame)); err != nil {
		return blast.Inputs{}, err
	}
	lf, err := os.Open(leasesPath)
	if err != nil {
		return blast.Inputs{}, err
	}
	defer lf.Close()
	if in.IDs, err = discovery.LoadIdentities(lf); err != nil {
		return blast.Inputs{}, fmt.Errorf("%s: %w", leasesPath, err)
	}
	rf, err := os.Open(rulesPath)
	if err != nil {
		return blast.Inputs{}, err
	}
	defer rf.Close()
	if in.Rules, err = policy.Load(rf); err != nil {
		return blast.Inputs{}, fmt.Errorf("%s: %w", rulesPath, err)
	}
	if nb, err := newNetbox(); err == nil {
		in.Tags = nb
	}
	if in.Prev, in.Cur, err = pollTwice(cfg, in.Rules.LivenessWindow); err != nil {
		return blast.Inputs{}, err
	}
	return in, nil
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
