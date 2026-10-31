package drift

import (
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/triadstudios/owl/internal/finding"
	"github.com/triadstudios/owl/internal/perception"
)

const minCableOverlap = 0.5

func Compare(before, after *perception.Result, now time.Time) []finding.Finding {
	was, is := byNum(before), byNum(after)
	var nums []int
	for n := range was {
		nums = append(nums, n)
	}
	for n := range is {
		if _, ok := was[n]; !ok {
			nums = append(nums, n)
		}
	}
	slices.Sort(nums)

	var out []finding.Finding
	for _, n := range nums {
		b, inBefore := was[n]
		a, inAfter := is[n]
		from, to := occupancy(b, inBefore), occupancy(a, inAfter)
		var diff string
		switch {
		case from != to:
			diff = fmt.Sprintf("%s -> %s", from, to)
		case from == "occupied":
			if overlap := boxIoU(bounds(b.CablePolygon), bounds(a.CablePolygon)); overlap < minCableOverlap {
				diff = fmt.Sprintf("cable changed: outline boxes overlap %.2f < %.2f", overlap, minCableOverlap)
			}
		}
		if diff != "" {
			out = append(out, finding.Finding{Type: "drift", Port: n, TS: now.UTC(), BeforeFrame: before.FrameHash, AfterFrame: after.FrameHash, Diff: diff})
		}
	}
	return out
}

func byNum(r *perception.Result) map[int]perception.Port {
	out := map[int]perception.Port{}
	for _, p := range r.Ports {
		out[p.Num] = p
	}
	return out
}

func occupancy(p perception.Port, seen bool) string {
	switch {
	case !seen:
		return "not seen"
	case p.Occupied:
		return "occupied"
	default:
		return "empty"
	}
}

func bounds(points [][2]float64) [4]float64 {
	if len(points) == 0 {
		return [4]float64{}
	}
	b := [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, p := range points {
		b[0], b[1] = min(b[0], p[0]), min(b[1], p[1])
		b[2], b[3] = max(b[2], p[0]), max(b[3], p[1])
	}
	return b
}

func boxIoU(a, b [4]float64) float64 {
	inter := max(0, min(a[2], b[2])-max(a[0], b[0])) * max(0, min(a[3], b[3])-max(a[1], b[1]))
	union := (a[2]-a[0])*(a[3]-a[1]) + (b[2]-b[0])*(b[3]-b[1]) - inter
	if union <= 0 {
		return 0
	}
	return inter / union
}
