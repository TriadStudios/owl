package qr

import (
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
)

const quiet = 4

var levelM = []struct{ ecc, n1, d1, n2, d2 int }{
	{10, 1, 16, 0, 0},
	{16, 1, 28, 0, 0},
	{26, 1, 44, 0, 0},
	{18, 2, 32, 0, 0},
	{24, 2, 43, 0, 0},
	{16, 4, 27, 0, 0},
	{18, 4, 31, 0, 0},
	{22, 2, 38, 2, 39},
	{22, 3, 36, 2, 37},
	{26, 4, 43, 1, 44},
	{30, 1, 50, 4, 51},
	{22, 6, 36, 2, 37},
	{22, 8, 37, 1, 38},
	{24, 4, 40, 5, 41},
	{24, 5, 41, 5, 42},
}

func Encode(data []byte) ([][]bool, error) {
	for v := 1; v <= len(levelM); v++ {
		spec := levelM[v-1]
		countBits := 8
		if v >= 10 {
			countBits = 16
		}
		if 4+countBits+8*len(data) <= 8*(spec.n1*spec.d1+spec.n2*spec.d2) {
			return build(v, countBits, data), nil
		}
	}
	return nil, errors.New("qr: payload too large")
}

type bits []bool

func (b *bits) put(v, n int) {
	for i := n - 1; i >= 0; i-- {
		*b = append(*b, v>>i&1 == 1)
	}
}

func build(v, countBits int, data []byte) [][]bool {
	spec := levelM[v-1]
	capacity := spec.n1*spec.d1 + spec.n2*spec.d2
	var bb bits
	bb.put(0b0100, 4)
	bb.put(len(data), countBits)
	for _, c := range data {
		bb.put(int(c), 8)
	}
	bb.put(0, min(4, capacity*8-len(bb)))
	bb.put(0, (8-len(bb)%8)%8)
	for pad := 0xEC; len(bb) < capacity*8; pad ^= 0xEC ^ 0x11 {
		bb.put(pad, 8)
	}
	codewords := make([]byte, capacity)
	for i, on := range bb {
		if on {
			codewords[i/8] |= 0x80 >> (i % 8)
		}
	}

	divisor := rsDivisor(spec.ecc)
	var blocks, eccs [][]byte
	for i, off := 0, 0; i < spec.n1+spec.n2; i++ {
		n := spec.d1
		if i >= spec.n1 {
			n = spec.d2
		}
		blocks = append(blocks, codewords[off:off+n])
		eccs = append(eccs, rsRemainder(codewords[off:off+n], divisor))
		off += n
	}
	var stream []byte
	for i := range max(spec.d1, spec.d2) {
		for _, b := range blocks {
			if i < len(b) {
				stream = append(stream, b[i])
			}
		}
	}
	for i := range spec.ecc {
		for _, e := range eccs {
			stream = append(stream, e[i])
		}
	}

	s := newSymbol(v)
	s.place(stream)
	best, bestScore := 0, -1
	for mask := range 8 {
		s.apply(mask)
		s.format(mask)
		if score := s.penalty(); bestScore < 0 || score < bestScore {
			best, bestScore = mask, score
		}
		s.apply(mask)
	}
	s.apply(best)
	s.format(best)
	return s.dark
}

func gfMul(x, y byte) byte {
	z := 0
	for i := 7; i >= 0; i-- {
		z = z<<1 ^ (z>>7)*0x11D
		z ^= (int(y) >> i & 1) * int(x)
	}
	return byte(z)
}

func rsDivisor(degree int) []byte {
	out := make([]byte, degree)
	out[degree-1] = 1
	root := byte(1)
	for range degree {
		for j := range out {
			out[j] = gfMul(out[j], root)
			if j+1 < len(out) {
				out[j] ^= out[j+1]
			}
		}
		root = gfMul(root, 2)
	}
	return out
}

func rsRemainder(data, divisor []byte) []byte {
	out := make([]byte, len(divisor))
	for _, b := range data {
		factor := b ^ out[0]
		copy(out, out[1:])
		out[len(out)-1] = 0
		for i, d := range divisor {
			out[i] ^= gfMul(d, factor)
		}
	}
	return out
}

type symbol struct {
	size        int
	dark, fixed [][]bool
}

func newSymbol(v int) *symbol {
	size := 4*v + 17
	s := &symbol{size: size, dark: grid(size), fixed: grid(size)}
	for i := range size {
		s.set(6, i, i%2 == 0)
		s.set(i, 6, i%2 == 0)
	}
	for _, c := range [][2]int{{3, 3}, {size - 4, 3}, {3, size - 4}} {
		for dy := -4; dy <= 4; dy++ {
			for dx := -4; dx <= 4; dx++ {
				x, y := c[0]+dx, c[1]+dy
				if x >= 0 && x < size && y >= 0 && y < size {
					d := max(abs(dx), abs(dy))
					s.set(x, y, d != 2 && d != 4)
				}
			}
		}
	}
	pos := alignment(v)
	last := len(pos) - 1
	for i, ay := range pos {
		for j, ax := range pos {
			if i == 0 && j == 0 || i == 0 && j == last || i == last && j == 0 {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					s.set(ax+dx, ay+dy, max(abs(dx), abs(dy)) != 1)
				}
			}
		}
	}
	s.format(0)
	if v >= 7 {
		rem := v
		for range 12 {
			rem = rem<<1 ^ (rem>>11)*0x1F25
		}
		code := v<<12 | rem
		for i := range 18 {
			on := code>>i&1 == 1
			a, b := size-11+i%3, i/3
			s.set(a, b, on)
			s.set(b, a, on)
		}
	}
	return s
}

func alignment(v int) []int {
	if v == 1 {
		return nil
	}
	n := v/7 + 2
	step := (v*4 + n*2 + 1) / (n*2 - 2) * 2
	pos := make([]int, n)
	pos[0] = 6
	for i, p := n-1, v*4+10; i >= 1; i, p = i-1, p-step {
		pos[i] = p
	}
	return pos
}

func (s *symbol) set(x, y int, on bool) {
	s.dark[y][x] = on
	s.fixed[y][x] = true
}

func (s *symbol) format(mask int) {
	rem := mask
	for range 10 {
		rem = rem<<1 ^ (rem>>9)*0x537
	}
	code := (mask<<10 | rem) ^ 0x5412
	bit := func(i int) bool { return code>>i&1 == 1 }
	for i := range 6 {
		s.set(8, i, bit(i))
	}
	s.set(8, 7, bit(6))
	s.set(8, 8, bit(7))
	s.set(7, 8, bit(8))
	for i := 9; i < 15; i++ {
		s.set(14-i, 8, bit(i))
	}
	for i := range 8 {
		s.set(s.size-1-i, 8, bit(i))
	}
	for i := 8; i < 15; i++ {
		s.set(8, s.size-15+i, bit(i))
	}
	s.set(8, s.size-8, true)
}

func (s *symbol) place(stream []byte) {
	i := 0
	for right := s.size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := range s.size {
			for j := range 2 {
				x, y := right-j, vert
				if (right+1)&2 == 0 {
					y = s.size - 1 - vert
				}
				if !s.fixed[y][x] && i < len(stream)*8 {
					s.dark[y][x] = stream[i>>3]>>(7-i&7)&1 == 1
					i++
				}
			}
		}
	}
}

func (s *symbol) apply(mask int) {
	for y := range s.size {
		for x := range s.size {
			var flip bool
			switch mask {
			case 0:
				flip = (x+y)%2 == 0
			case 1:
				flip = y%2 == 0
			case 2:
				flip = x%3 == 0
			case 3:
				flip = (x+y)%3 == 0
			case 4:
				flip = (x/3+y/2)%2 == 0
			case 5:
				flip = x*y%2+x*y%3 == 0
			case 6:
				flip = (x*y%2+x*y%3)%2 == 0
			case 7:
				flip = ((x+y)%2+x*y%3)%2 == 0
			}
			if flip && !s.fixed[y][x] {
				s.dark[y][x] = !s.dark[y][x]
			}
		}
	}
}

func (s *symbol) penalty() int {
	score := 0
	line := func(at func(int) bool) {
		run := 0
		for i := range s.size {
			if i > 0 && at(i) == at(i-1) {
				run++
			} else {
				run = 1
			}
			if run == 5 {
				score += 3
			} else if run > 5 {
				score++
			}
		}
		for i := 0; i+7 <= s.size; i++ {
			if !(at(i) && !at(i+1) && at(i+2) && at(i+3) && at(i+4) && !at(i+5) && at(i+6)) {
				continue
			}
			before, after := true, true
			for k := 1; k <= 4; k++ {
				before = before && (i-k < 0 || !at(i-k))
				after = after && (i+6+k >= s.size || !at(i+6+k))
			}
			if before || after {
				score += 40
			}
		}
	}
	dark := 0
	for y := range s.size {
		line(func(x int) bool { return s.dark[y][x] })
		line(func(x int) bool { return s.dark[x][y] })
		for x := range s.size {
			if s.dark[y][x] {
				dark++
			}
			if x+1 < s.size && y+1 < s.size {
				c := s.dark[y][x]
				if c == s.dark[y][x+1] && c == s.dark[y+1][x] && c == s.dark[y+1][x+1] {
					score += 3
				}
			}
		}
	}
	score += abs(dark*100/(s.size*s.size)-50) / 5 * 10
	return score
}

func grid(n int) [][]bool {
	g := make([][]bool, n)
	for i := range g {
		g[i] = make([]bool, n)
	}
	return g
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func dark(m [][]bool, x, y int) bool {
	return y >= 0 && y < len(m) && x >= 0 && x < len(m) && m[y][x]
}

// Black on an explicit white background, so the code scans on dark terminal themes too.
func Terminal(m [][]bool) string {
	var b strings.Builder
	for y := -quiet; y < len(m)+quiet; y += 2 {
		b.WriteString("\x1b[30;47m")
		for x := -quiet; x < len(m)+quiet; x++ {
			switch top, bottom := dark(m, x, y), dark(m, x, y+1); {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString("\x1b[0m\n")
	}
	return b.String()
}

func PNG(w io.Writer, m [][]bool, scale int) error {
	side := (len(m) + 2*quiet) * scale
	img := image.NewGray(image.Rect(0, 0, side, side))
	for py := range side {
		for px := range side {
			c := color.Gray{Y: 255}
			if dark(m, px/scale-quiet, py/scale-quiet) {
				c.Y = 0
			}
			img.SetGray(px, py, c)
		}
	}
	return png.Encode(w, img)
}
