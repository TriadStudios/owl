package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
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
	"github.com/triadstudios/owl/internal/drift"
	"github.com/triadstudios/owl/internal/enroll"
	"github.com/triadstudios/owl/internal/finding"
	"github.com/triadstudios/owl/internal/guardian"
	"github.com/triadstudios/owl/internal/job"
	"github.com/triadstudios/owl/internal/narrate"
	"github.com/triadstudios/owl/internal/netbox"
	"github.com/triadstudios/owl/internal/perception"
	"github.com/triadstudios/owl/internal/policy"
	"github.com/triadstudios/owl/internal/qr"
	"github.com/triadstudios/owl/internal/reconcile"
	"github.com/triadstudios/owl/internal/ui"
)

const (
	passiveWindows    = 3
	pulseOn           = 4 * time.Second
	pulseOff          = 4 * time.Second
	pulseCycles       = 5
	pulseSample       = time.Second
	maxPingTargets    = 4
	pulseRate         = 256 << 10
	pingPayload       = 64000
	enrollBurst       = 10 * time.Second
	touchMinChange    = 12.0
	touchDominance    = 1.5
	touchStripDepth   = 1.0
	baselinePath      = "data/baseline.json"
	devicesPath       = "data/devices.json"
	jobsPath          = "data/jobs.json"
	findingsPath      = "data/findings.json"
	framesDir         = "data/frames"
	tlsDir            = "data/tls"
	evidenceDir       = "data/evidence"
	trackFPS          = 10
	benchCaptureTries = 3
)

const usage = "usage: owl snmp walk|dump [-host H] [-community C] [-out FILE] | owl reconcile -image FILE [-leases CSV] [-perception URL] [-rules FILE] | owl blast -port N -image FILE [-leases CSV] [-rules FILE] | owl enroll | owl diff -image FILE [-perception URL] | owl bench -expect XXX...X. [-captures 20] [-frames 5] [-every 30s] | owl watch [-leases CSV] [-robot URL] | owl netbox seed [-leases CSV] [-uplink NAMES] [-critical NAMES] | owl policy lint [-rules FILE] | owl serve [-addr A] [-image FILE] [-leases CSV] [-rules FILE] [-robot URL] | owl pair [-name N] [-url wss://HOST:PORT] | owl pair list | owl pair revoke NAME | owl job add -port N -tech NAME -window HH:MM-HH:MM [-note TEXT] | owl job list | owl job close ID"

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
	case len(args) >= 2 && args[0] == "pair" && (args[1] == "list" || args[1] == "revoke"):
		cmd, args = args[0]+" "+args[1], args[2:]
	case len(args) >= 2 && args[0] == "job" && (args[1] == "add" || args[1] == "list" || args[1] == "close"):
		cmd, args = args[0]+" "+args[1], args[2:]
	case len(args) >= 1 && (args[0] == "reconcile" || args[0] == "blast" || args[0] == "enroll" || args[0] == "diff" || args[0] == "bench" || args[0] == "watch" || args[0] == "serve" || args[0] == "pair"):
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
	addr := fset.String("addr", "0.0.0.0:8080", "serve listen address")
	robotURL := fset.String("robot", "http://127.0.0.1:8702", "robot bridge URL")
	pairName := fset.String("name", "phone", "paired device name")
	captures := fset.Int("captures", 20, "bench captures")
	burstFrames := fset.Int("frames", 5, "bench frames per capture, majority vote")
	every := fset.Duration("every", 30*time.Second, "bench time between captures")
	expect := fset.String("expect", "", "bench expected occupancy left to right, X plugged . empty")
	tech := fset.String("tech", "", "technician doing the job")
	window := fset.String("window", "", "job window HH:MM-HH:MM, today local; an end before the start is tomorrow")
	note := fset.String("note", "", "job note")
	pairURL := fset.String("url", "", "relay URL in the pairing QR (default wss://<this box's address toward the switch>:<addr port>)")
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
	case "diff":
		if *image == "" {
			return errors.New("diff needs -image FILE")
		}
		data, err := os.ReadFile(baselinePath)
		if err != nil {
			return fmt.Errorf("%w; run owl enroll first", err)
		}
		var baseline enroll.Baseline
		if err := json.Unmarshal(data, &baseline); err != nil {
			return fmt.Errorf("%s: %w", baselinePath, err)
		}
		cam := perception.Client{BaseURL: *perceptionURL, HTTP: &http.Client{Timeout: 6 * time.Minute}}
		ctx := context.Background()
		segment := func(path string) (*perception.Result, []byte, error) {
			frame, err := os.ReadFile(path)
			if err != nil {
				return nil, nil, err
			}
			res, err := cam.Segment(ctx, frame, http.DetectContentType(frame))
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", path, err)
			}
			return res, frame, nil
		}
		before, beforeFrame, err := segment(filepath.Join(framesDir, baseline.FrameHash+".png"))
		if err != nil {
			return err
		}
		after, afterFrame, err := segment(*image)
		if err != nil {
			return err
		}
		found := drift.Compare(before, after, time.Now())
		jobs, err := job.Load(jobsPath)
		if err != nil {
			return err
		}
		for i := range found {
			if j := job.Covering(jobs, found[i].Port, found[i].TS); j != nil {
				found[i].JobID = j.ID
			}
		}
		if len(found) > 0 {
			for _, ev := range []struct {
				hash  string
				frame []byte
			}{{before.FrameHash, beforeFrame}, {after.FrameHash, afterFrame}} {
				if err := saveEvidence(ev.hash, ev.frame); err != nil {
					return err
				}
			}
			if err := finding.Append(findingsPath, found); err != nil {
				return err
			}
		}
		fmt.Printf("baseline %s (enrolled %s) vs %s\n", before.FrameHash[:16], baseline.EnrolledAt.Local().Format(time.DateTime), after.FrameHash[:16])
		if len(found) == 0 {
			fmt.Println("no drift")
			return nil
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "PORT\tDIFF\tJOB")
		for _, f := range found {
			covered := "none (deviation)"
			if f.JobID != 0 {
				covered = strconv.Itoa(f.JobID)
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\n", f.Port, f.Diff, covered)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		fmt.Printf("%d finding(s) appended to %s\n", len(found), findingsPath)
		return nil
	case "bench":
		return bench(*perceptionURL, *captures, *burstFrames, *every, *expect)
	case "watch":
		return watch(cfg, *perceptionURL, *leases, *rulesPath, *robotURL)
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
	case "serve":
		return serve(cfg, *addr, *image, *perceptionURL, *leases, *rulesPath, *robotURL)
	case "pair":
		return pair(cfg, *pairName, *pairURL, *addr)
	case "pair list":
		devices, err := ui.LoadDevices(devicesPath)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tCREATED\tLAST SEEN")
		for _, d := range devices {
			seen := "never"
			if !d.LastSeen.IsZero() {
				seen = d.LastSeen.Local().Format(time.DateTime)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\n", d.Name, d.Created.Local().Format(time.DateTime), seen)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		stale, err := ui.StalePairCodes(devicesPath, time.Now())
		if err != nil {
			return err
		}
		for _, code := range stale {
			fmt.Printf("warning: %s is over 10 min old and unused; it holds a live token, scan it or delete it\n", code)
		}
		return nil
	case "pair revoke":
		if fset.NArg() != 1 {
			return errors.New("usage: owl pair revoke NAME")
		}
		if err := ui.Revoke(devicesPath, fset.Arg(0)); err != nil {
			return err
		}
		fmt.Printf("revoked %s\n", fset.Arg(0))
		return nil
	case "job add":
		start, end, err := job.ParseWindow(*window, time.Now())
		if err != nil {
			return err
		}
		j, err := job.Add(jobsPath, job.Job{Port: *port, Tech: *tech, Start: start, End: end, Note: *note, Created: time.Now().UTC()})
		if err != nil {
			return err
		}
		fmt.Printf("job %d: port %d, %s, %s-%s\n", j.ID, j.Port, j.Tech, j.Start.Format("Jan 2 15:04"), j.End.Format("Jan 2 15:04"))
		return nil
	case "job list":
		jobs, err := job.Load(jobsPath)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tPORT\tTECH\tWINDOW\tSTATE\tNOTE")
		for _, j := range jobs {
			state := "open"
			if !j.Closed.IsZero() {
				state = "closed " + j.Closed.Local().Format("Jan 2 15:04")
			}
			fmt.Fprintf(tw, "%d\t%d\t%s\t%s-%s\t%s\t%s\n", j.ID, j.Port, j.Tech, j.Start.Local().Format("Jan 2 15:04"), j.End.Local().Format("Jan 2 15:04"), state, j.Note)
		}
		return tw.Flush()
	case "job close":
		if fset.NArg() != 1 {
			return errors.New("usage: owl job close ID")
		}
		id, err := strconv.Atoi(fset.Arg(0))
		if err != nil {
			return fmt.Errorf("job id %q: %w", fset.Arg(0), err)
		}
		if err := job.Close(jobsPath, id, time.Now()); err != nil {
			return err
		}
		fmt.Printf("closed job %d\n", id)
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

func serve(cfg discovery.Config, addr, image, perceptionURL, leasesPath, rulesPath, robotURL string) error {
	if leasesPath == "" {
		return errors.New("need -leases or OWL_DHCP_LEASES")
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	rf, err := os.Open(rulesPath)
	if err != nil {
		return err
	}
	rules, err := policy.Load(rf)
	rf.Close()
	if err != nil {
		return fmt.Errorf("%s: %w", rulesPath, err)
	}
	in, err := thresholds()
	if err != nil {
		return err
	}
	sc := ui.Config{
		Switch:         cfg,
		Perception:     &perception.Client{BaseURL: perceptionURL, HTTP: &http.Client{Timeout: 30 * time.Second}},
		RobotURL:       robotURL,
		LeasesPath:     leasesPath,
		Rules:          rules,
		AttributionMin: in.AttributionMin,
		AggregationMin: in.AggregationMin,
		EvidenceDir:    evidenceDir,
		FindingsPath:   findingsPath,
		Devices:        &ui.Registry{Path: devicesPath},
		Log:            log,
	}
	if nb, err := newNetbox(); err == nil {
		sc.Tags = nb
	}
	srv, err := ui.New(sc)
	if err != nil {
		return err
	}
	if image != "" {
		frame, err := os.ReadFile(image)
		if err != nil {
			return err
		}
		res, err := sc.Perception.Segment(context.Background(), frame, http.DetectContentType(frame))
		if err != nil {
			return fmt.Errorf("%s: %w", image, err)
		}
		srv.SetCamera(res)
		log.Info("camera seeded", "image", image, "frame_hash", res.FrameHash, "ports", len(res.Ports))
	}
	cert, fingerprint, err := ui.LoadOrCreateCert(tlsDir)
	if err != nil {
		return err
	}
	// The relay WebSocket hijacks the connection, which HTTP/2 does not allow.
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	hs := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		Protocols:         &protocols,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go srv.Run(ctx)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := hs.Shutdown(shutdown); err != nil {
			log.Warn("shutdown", "err", err)
		}
	}()
	log.Info("serving", "addr", addr, "cert_sha256", fingerprint)
	if err := hs.ListenAndServeTLS("", ""); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func pair(cfg discovery.Config, name, relayURL, addr string) error {
	if relayURL == "" {
		if cfg.Host == "" {
			return errors.New("need -url or OWL_SWITCH_HOST to pick the address the phone should dial")
		}
		// Dialing UDP sends nothing; it only asks the OS which local address routes toward the switch.
		conn, err := net.Dial("udp", net.JoinHostPort(cfg.Host, "161"))
		if err != nil {
			return err
		}
		local := conn.LocalAddr().(*net.UDPAddr).IP
		conn.Close()
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return err
		}
		relayURL = "wss://" + net.JoinHostPort(local.String(), port)
	}
	if !strings.HasPrefix(relayURL, "wss://") {
		return fmt.Errorf("-url %q: the relay app only accepts wss://", relayURL)
	}
	_, fingerprint, err := ui.LoadOrCreateCert(tlsDir)
	if err != nil {
		return err
	}
	token, err := ui.Pair(devicesPath, name, time.Now())
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"url": relayURL, "token": token, "cert_sha256": fingerprint})
	if err != nil {
		return err
	}
	code, err := qr.Encode(payload)
	if err != nil {
		return err
	}
	pngPath := ui.PairCodePath(devicesPath, name)
	f, err := os.OpenFile(pngPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := qr.PNG(f, code, 8); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Print(qr.Terminal(code))
	fmt.Printf("paired %s -> %s\ncert sha256 %s\nQR also in %s; it holds the token and is deleted on the device's first connection\n", name, relayURL, fingerprint, pngPath)
	return nil
}

type benchCapture struct {
	Capture int      `json:"capture"`
	Frames  []string `json:"frames"`
	Votes   []string `json:"votes"`
	Vote    string   `json:"vote"`
	OK      bool     `json:"ok"`
}

func bench(perceptionURL string, captures, frames int, every time.Duration, expect string) error {
	if !regexp.MustCompile(`^[X.]+$`).MatchString(expect) {
		return errors.New("bench needs -expect, e.g. XXX...X. for ports 1-3 and 7 plugged")
	}
	if captures < 1 || frames < 1 || frames > 10 {
		return errors.New("bench wants -captures >= 1 and -frames in 1..10")
	}
	cam := &perception.Client{BaseURL: perceptionURL, HTTP: &http.Client{Timeout: 6 * time.Minute}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	report := make([]benchCapture, captures)
	work := make(chan int, captures)
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range work {
			for _, hash := range report[i].Frames {
				vote := "rejected: "
				frame, err := os.ReadFile(filepath.Join(framesDir, hash+".png"))
				var res *perception.Result
				if err == nil {
					res, err = cam.Segment(ctx, frame, "image/png")
				}
				if err != nil {
					vote += err.Error()
				} else {
					var b strings.Builder
					for _, p := range res.Ports {
						if p.Occupied {
							b.WriteByte('X')
						} else {
							b.WriteByte('.')
						}
					}
					vote = b.String()
				}
				report[i].Votes = append(report[i].Votes, vote)
			}
		}
	})
	start := time.Now()
	for i := range captures {
		select {
		case <-ctx.Done():
		case <-time.After(time.Until(start.Add(time.Duration(i) * every))):
		}
		if ctx.Err() != nil {
			break
		}
		var hashes []string
		var err error
		for try := range benchCaptureTries {
			if hashes, err = cam.Capture(ctx, frames, 200*time.Millisecond); err == nil || ctx.Err() != nil {
				break
			}
			fmt.Printf("capture %d try %d failed: %v\n", i+1, try+1, err)
		}
		if err != nil {
			close(work)
			wg.Wait()
			return fmt.Errorf("capture %d: %w", i+1, err)
		}
		report[i] = benchCapture{Capture: i + 1, Frames: hashes}
		work <- i
		fmt.Printf("capture %d/%d at %s, %d frames\n", i+1, captures, time.Since(start).Round(time.Second), len(hashes))
	}
	close(work)
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}

	good, single, total := 0, 0, 0
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CAPTURE\tRESULT\tVOTE\tFRAMES")
	for i := range report {
		r := &report[i]
		counts := map[string]int{}
		for _, v := range r.Votes {
			counts[v]++
			total++
			if v == expect {
				single++
			}
		}
		r.Vote = "no majority"
		for v, n := range counts {
			if 2*n > len(r.Votes) {
				r.Vote = v
			}
		}
		r.OK = r.Vote == expect
		result := "FAIL"
		if r.OK {
			good++
			result = "PASS"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", r.Capture, result, r.Vote, strings.Join(r.Votes, " | "))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Printf("%d/%d captures voted %s; single frames %d/%d\n", good, captures, expect, single, total)
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll("data/bench", 0o755); err != nil {
		return err
	}
	out := filepath.Join("data/bench", start.Format("20060102T150405")+".json")
	if err := os.WriteFile(out, append(data, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Println("report", out)
	return nil
}

func watch(cfg discovery.Config, perceptionURL, leasesPath, rulesPath, robotURL string) error {
	if leasesPath == "" {
		return errors.New("need -leases or OWL_DHCP_LEASES")
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cam := perception.Client{BaseURL: perceptionURL, HTTP: &http.Client{Timeout: 6 * time.Minute}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	frame, err := cam.SegmentCamera(ctx, time.Second)
	if err != nil {
		return err
	}
	log.Info("camera", "frame_hash", frame.FrameHash, "ports", len(frame.Ports))
	in, err := loadInputs(cfg, frame, leasesPath, rulesPath)
	if err != nil {
		return err
	}
	ports, err := reconcile.Join(in)
	if err != nil {
		return err
	}
	var mu sync.Mutex
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Minute):
			}
			prev, cur, err := pollTwice(cfg, in.Rules.LivenessWindow)
			if err != nil {
				log.Error("snmp poll", "err", err)
				continue
			}
			next := in
			next.Prev, next.Cur = prev, cur
			fresh, err := reconcile.Join(next)
			if err != nil {
				log.Error("reconcile", "err", err)
				continue
			}
			mu.Lock()
			ports = fresh
			mu.Unlock()
		}
	}()
	bridge := &guardian.Bridge{BaseURL: robotURL, HTTP: &http.Client{Timeout: time.Minute}, Log: log}
	g := &guardian.Guardian{
		Robot:  bridge,
		Camera: bridge,
		Table: func() []reconcile.Port {
			mu.Lock()
			defer mu.Unlock()
			return ports
		},
		Jobs: func() []narrate.Job {
			jobs, err := job.Load(jobsPath)
			if err != nil {
				log.Error("jobs", "err", err)
				return nil
			}
			now := time.Now()
			var open []narrate.Job
			for _, j := range jobs {
				if j.Closed.IsZero() && !now.Before(j.Start) && !now.After(j.End) {
					open = append(open, narrate.Job{ID: j.ID, Port: j.Port, Tech: j.Tech, Note: j.Note, Until: j.End.Local().Format("15:04")})
				}
			}
			return open
		},
		Log: log,
	}
	bridge.Keyterms = func() []string {
		mu.Lock()
		defer mu.Unlock()
		return guardian.DeviceTerms(ports)
	}
	narrator := "templates only, OWL_META_API_KEY not set"
	if key := os.Getenv("OWL_META_API_KEY"); key != "" {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.IdleConnTimeout = 2 * time.Minute
		client := &narrate.Client{URL: narrate.DefaultURL, Model: narrate.DefaultModel, Key: key, HTTP: &http.Client{Timeout: 10 * time.Second, Transport: transport}}
		g.Narrator, narrator = client, narrate.DefaultModel
		go func() {
			for {
				if err := client.Warm(ctx); err != nil && ctx.Err() == nil {
					log.Warn("narrate warm", "err", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(20 * time.Second):
				}
			}
		}()
	}
	log.Info("guardian watching", "robot", robotURL, "narrator", narrator)
	trackClient := &perception.Client{BaseURL: perceptionURL, HTTP: &http.Client{}}
	reach := &guardian.Reach{
		Track: func(ctx context.Context) (<-chan perception.TrackFrame, error) {
			return trackClient.Track(ctx, frame.Row, trackFPS)
		},
		Alert: bridge.Alert,
		Clip:  func() (string, error) { return cam.Clip(ctx) },
		Log:   log,
	}
	startReach := func(a *guardian.Approach, cur []reconcile.Port) context.CancelFunc {
		rctx, cancel := context.WithCancel(ctx)
		go reach.Run(rctx, a, cur)
		return cancel
	}
	var stopReach context.CancelFunc
	defer func() {
		if stopReach != nil {
			stopReach()
		}
	}()
	state := g.State()
	for ctx.Err() == nil {
		g.Step(time.Now())
		if g.State() != state {
			log.Info("guardian", "from", state, "to", g.State())
			state = g.State()
			switch {
			case state == guardian.Answer && stopReach == nil:
				mu.Lock()
				cur := ports
				mu.Unlock()
				a := approachFor(frame, cur, log)
				bridge.Home()
				lines := guardian.WarningLines(a, cur)
				go func() {
					if err := bridge.Prepare(lines); err != nil {
						log.Warn("prepare warnings", "err", err)
					}
				}()
				log.Info("reach watch", "do_not_touch", a.DoNotTouch, "populated", a.Populated, "jobs", a.JobPorts)
				stopReach = startReach(a, cur)
			case (state == guardian.Sleep || state == guardian.Idle) && stopReach != nil:
				stopReach()
				stopReach = nil
				log.Info("reach watch stopped")
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(200 * time.Millisecond):
		}
	}
	return nil
}

func approachFor(frame *perception.Result, ports []reconcile.Port, log *slog.Logger) *guardian.Approach {
	a := &guardian.Approach{RowY0: frame.Row[0], RowY1: frame.Row[1], DoNotTouch: map[int]bool{}, Populated: map[int]bool{}, JobPorts: map[int]int{}}
	for _, c := range frame.Ports {
		a.Ports = append(a.Ports, guardian.PortBox{Num: c.Num, X0: c.Box[0], X1: c.Box[2]})
		if c.Occupied {
			a.Populated[c.Num] = true
		}
	}
	for _, p := range ports {
		if p.Blast != nil && p.Blast.Unknown == "" && p.Blast.Verdict == blast.DoNotTouch {
			a.DoNotTouch[p.Num] = true
		}
	}
	jobs, err := job.Load(jobsPath)
	if err != nil {
		log.Error("jobs", "err", err)
	}
	now := time.Now()
	for _, j := range jobs {
		if j.Closed.IsZero() && !now.Before(j.Start) && !now.After(j.End) {
			a.JobPorts[j.Port] = j.ID
		}
	}
	return a
}

func saveEvidence(hash string, frame []byte) error {
	ext := ".png"
	if http.DetectContentType(frame) == "image/jpeg" {
		ext = ".jpg"
	}
	if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(evidenceDir, hash+ext), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = f.Write(frame)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

func thresholds() (blast.Inputs, error) {
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
	return in, nil
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
	frame, err := os.ReadFile(image)
	if err != nil {
		return blast.Inputs{}, err
	}
	client := perception.Client{BaseURL: perceptionURL, HTTP: &http.Client{Timeout: 6 * time.Minute}}
	cam, err := client.Segment(context.Background(), frame, http.DetectContentType(frame))
	if err != nil {
		return blast.Inputs{}, err
	}
	return loadInputs(cfg, cam, leasesPath, rulesPath)
}

func loadInputs(cfg discovery.Config, cam *perception.Result, leasesPath, rulesPath string) (blast.Inputs, error) {
	in, err := thresholds()
	if err != nil {
		return blast.Inputs{}, err
	}
	in.Camera = cam
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
