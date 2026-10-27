package reconcile

import (
	"slices"

	"github.com/triadstudios/owl/internal/blast"
	"github.com/triadstudios/owl/internal/discovery"
	"github.com/triadstudios/owl/internal/perception"
	"github.com/triadstudios/owl/internal/policy"
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
	Blast   *blast.Card
	Sources []string
}

func Join(cam *perception.Result, prev, cur *discovery.SwitchState, ids *discovery.Identities, rules policy.Rules) ([]Port, error) {
	camByNum := map[int]perception.Port{}
	var nums []int
	for _, p := range cam.Ports {
		camByNum[p.Num] = p
		nums = append(nums, p.Num)
	}
	for n := range cur.Ports {
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
		s, onSwitch := cur.Ports[n]
		if onSwitch {
			port.Switch = "down"
			if s.OperUp {
				port.Switch = "up"
			}
			for _, src := range s.Sources {
				port.Sources = append(port.Sources, src)
			}
			card, err := blast.Assess(n, prev, cur, ids, rules)
			if err != nil {
				return nil, err
			}
			port.Blast = &card
			for _, d := range card.Devices {
				port.Sources = append(port.Sources, d.Sources...)
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
	return out, nil
}
