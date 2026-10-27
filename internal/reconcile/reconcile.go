package reconcile

import (
	"fmt"
	"slices"
	"strings"

	"github.com/triadstudios/owl/internal/discovery"
	"github.com/triadstudios/owl/internal/perception"
)

type Status string

const (
	Consistent   Status = "consistent"
	OccupiedDown Status = "occupied-down"
	EmptyUp      Status = "empty-up"
	Unknown      Status = "unknown"
)

type Port struct {
	Num     int
	Camera  string
	Switch  string
	Status  Status
	Devices []discovery.Device
	Summary string
	Sources []string
}

func Join(cam *perception.Result, sw *discovery.SwitchState, ids *discovery.Identities) []Port {
	camByNum := map[int]perception.Port{}
	var nums []int
	for _, p := range cam.Ports {
		camByNum[p.Num] = p
		nums = append(nums, p.Num)
	}
	for n := range sw.Ports {
		if _, seen := camByNum[n]; !seen {
			nums = append(nums, n)
		}
	}
	slices.Sort(nums)

	out := make([]Port, 0, len(nums))
	for _, n := range nums {
		port := Port{Num: n, Status: Unknown}
		c, onCamera := camByNum[n]
		if onCamera {
			port.Camera = "empty"
			if c.Occupied {
				port.Camera = "occupied"
			}
			port.Sources = append(port.Sources, "camera")
		}
		s, onSwitch := sw.Ports[n]
		if onSwitch {
			port.Switch = "down"
			if s.OperUp {
				port.Switch = "up"
			}
			for _, src := range s.Sources {
				port.Sources = append(port.Sources, src)
			}
			for _, mac := range s.MACs {
				d := ids.Lookup(mac)
				port.Devices = append(port.Devices, d)
				port.Sources = append(port.Sources, d.Sources...)
			}
			neighbor := ""
			if s.LLDP != nil {
				neighbor = s.LLDP.SysName
				if neighbor == "" {
					neighbor = s.LLDP.ChassisID
				}
			}
			switch {
			case len(port.Devices) > 1:
				var named []string
				for _, d := range port.Devices {
					if d.Hostname != "" && d.Hostname != neighbor {
						named = append(named, d.Hostname)
					}
				}
				slices.Sort(named)
				port.Summary = strings.TrimSpace(fmt.Sprintf("%d devices behind %s", len(port.Devices), neighbor))
				if len(named) > 0 {
					port.Summary += ": " + strings.Join(named, ", ")
				}
			case len(port.Devices) == 1 && port.Devices[0].Hostname != "":
				port.Summary = port.Devices[0].Hostname
			case neighbor != "":
				port.Summary = neighbor
			case len(port.Devices) == 1:
				port.Summary = strings.TrimSpace(port.Devices[0].Vendor + " " + port.Devices[0].MAC)
			}
		}
		if onCamera && onSwitch {
			switch {
			case c.Occupied == s.OperUp:
				port.Status = Consistent
			case c.Occupied:
				port.Status = OccupiedDown
			default:
				port.Status = EmptyUp
			}
		}
		slices.Sort(port.Sources)
		port.Sources = slices.Compact(port.Sources)
		out = append(out, port)
	}
	return out
}
