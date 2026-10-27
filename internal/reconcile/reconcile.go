package reconcile

import (
	"slices"

	"github.com/triadstudios/owl/internal/blast"
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
	Blast   *blast.Card
	Sources []string
}

func Join(in blast.Inputs) ([]Port, error) {
	camByNum := map[int]perception.Port{}
	var nums []int
	for _, p := range in.Camera.Ports {
		camByNum[p.Num] = p
		nums = append(nums, p.Num)
	}
	for n := range in.Cur.Ports {
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
		s, onSwitch := in.Cur.Ports[n]
		if onSwitch {
			port.Switch = "down"
			if s.OperUp {
				port.Switch = "up"
			}
			for _, src := range s.Sources {
				port.Sources = append(port.Sources, src)
			}
			card, err := blast.Assess(n, in)
			if err != nil {
				return nil, err
			}
			port.Blast = &card
			for _, d := range card.Devices {
				port.Sources = append(port.Sources, d.Sources...)
			}
		}
		if onCamera && onSwitch && port.Blast.Unknown == "" {
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
