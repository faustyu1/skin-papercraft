package papercraft

import (
	"math/bits"
)

// Free space is tracked on a coarse grid; each grid row is a bitmask, cell x is bit x.

// Bits is a row of grid cells, bit x of word x/64 is cell x.
type Bits []uint64

func newBits(n int) Bits { return make(Bits, (n+63)/64) }

func (b Bits) bit(i int) bool {
	return i/64 < len(b) && b[i/64]>>(i%64)&1 == 1
}

func (b Bits) set(i int) { b[i/64] |= 1 << (i % 64) }

func (b Bits) empty() bool {
	for _, w := range b {
		if w != 0 {
			return false
		}
	}
	return true
}

// orShifted sets dst |= src >> k (k may be negative for a left shift); bits past the end of
// dst are dropped.
func orShifted(dst, src Bits, k int) {
	if k < 0 {
		k = -k
		ws, bs := k/64, uint(k%64)
		for i := len(dst) - 1; i >= ws; i-- {
			j := i - ws
			var v uint64
			if j < len(src) {
				v = src[j] << bs
			}
			if bs != 0 && j-1 >= 0 && j-1 < len(src) {
				v |= src[j-1] >> (64 - bs)
			}
			dst[i] |= v
		}
		return
	}
	ws, bs := k/64, uint(k%64)
	for i := range dst {
		j := i + ws
		if j >= len(src) {
			break
		}
		v := src[j] >> bs
		if bs != 0 && j+1 < len(src) {
			v |= src[j+1] << (64 - bs)
		}
		dst[i] |= v
	}
}

// CellRows marks the grid cells that any non-zero mask pixel touches.
func CellRows(mask *Gray, cell int) ([]Bits, int, int) {
	mw, mh := mask.Rect.Dx(), mask.Rect.Dy()
	w := (mw + cell - 1) / cell
	h := (mh + cell - 1) / cell
	rows := make([]Bits, h)
	sums := make([]int, w)
	counts := make([]int, w)
	for cy := 0; cy < h; cy++ {
		clear(sums)
		clear(counts)
		for yy := cy * cell; yy < min((cy+1)*cell, mh); yy++ {
			line := mask.Pix[yy*mask.Stride : yy*mask.Stride+mw]
			for cx := range w {
				part := line[cx*cell : min((cx+1)*cell, mw)]
				sum := 0
				for _, v := range part {
					sum += int(v)
				}
				sums[cx] += sum
				counts[cx] += len(part)
			}
		}
		row := newBits(w)
		for cx := range w {
			if sums[cx]*2 >= counts[cx] { // Image.reduce rounds; anything roundable to 1 is taken
				row.set(cx)
			}
		}
		rows[cy] = row
	}
	return rows, w, h
}

// Dilate grows a mask by gap cells on every side (it gets 2*gap bigger).
func Dilate(rows []Bits, w, h, gap int) ([]Bits, int, int) {
	width := w + 2*gap
	grown := make([]Bits, 0, len(rows)+2*gap)
	for _, r := range rows {
		v := newBits(width)
		orShifted(v, r, -gap)
		t := newBits(width)
		for i := 0; i < gap; i++ {
			clear(t)
			orShifted(t, v, -1)
			orShifted(t, v, 1)
			for k := range v {
				v[k] |= t[k]
			}
		}
		grown = append(grown, v)
	}
	for i := 0; i < gap; i++ {
		grown = append(grown, newBits(width))
	}
	out := make([]Bits, len(grown))
	for i := range grown {
		v := newBits(width)
		for j := max(0, i-gap); j < min(len(grown), i+gap+1); j++ {
			for k := range v {
				v[k] |= grown[j][k]
			}
		}
		out[i] = v
	}
	return out, width, h + 2*gap
}

// Run is a run of set bits: first bit and length.
type Run struct{ Bit, Len int }

func Runs(row Bits) []Run {
	var out []Run
	n := len(row) * 64
	for bit := 0; bit < n; {
		if !row.bit(bit) {
			bit++
			continue
		}
		start := bit
		for bit < n && row.bit(bit) {
			bit++
		}
		out = append(out, Run{start, bit - start})
	}
	return out
}

// Shape is a sprite's footprint on the grid, grown by the gap.
type Shape struct {
	Rows  []Bits
	W, H  int
	lines []shapeLine // non-empty rows
}

type shapeLine struct {
	i    int
	runs []Run
}

func newShape(alpha *Gray, cell, gap int) *Shape {
	rows, w, h := CellRows(alpha, cell)
	rows, w, h = Dilate(rows, w, h, gap)
	s := &Shape{Rows: rows, W: w, H: h}
	for i, r := range rows {
		if !r.empty() {
			s.lines = append(s.lines, shapeLine{i, Runs(r)})
		}
	}
	return s
}

// FirstFit finds the top-most, then left-most spot where the piece overlaps nothing.
func FirstFit(occ []Bits, gridW, gridH int, s *Shape) (int, int, bool) {
	if s.W > gridW || s.H > gridH {
		return 0, 0, false
	}
	nx := gridW - s.W + 1 // x positions to try: bits 0..nx-1
	words := (nx + 63) / 64
	xs := make(Bits, words)
	for i := range xs {
		xs[i] = ^uint64(0)
	}
	if r := nx % 64; r != 0 {
		xs[words-1] = 1<<r - 1
	}
	blocked := make(Bits, words)
	// Smearing pulls bits down from past the tried positions, so t spans the whole row.
	t := make(Bits, max(words, len(occ[0])))
	for y := 0; y <= gridH-s.H; y++ {
		clear(blocked)
		for _, sl := range s.lines {
			o := occ[y+sl.i]
			for _, run := range sl.runs {
				// Cell x is blocked when any occupied cell lies in x+run.Bit .. x+run.Bit+run.Len-1.
				clear(t)
				orShifted(t, o, run.Bit)
				for done := 1; done < run.Len; {
					step := min(done, run.Len-done)
					orShifted(t, t, step)
					done += step
				}
				for k := range blocked {
					blocked[k] |= t[k]
				}
			}
			full := true
			for k := range blocked {
				if blocked[k]&xs[k] != xs[k] {
					full = false
					break
				}
			}
			if full {
				break
			}
		}
		for k := range xs {
			if free := xs[k] &^ blocked[k]; free != 0 {
				return k*64 + bits.TrailingZeros64(free), y, true
			}
		}
	}
	return 0, 0, false
}

// Packer places sprites onto pages. Each sprite's footprint is worked out once, however
// many pages it is tried on.
type Packer struct {
	cell, gap int
	shapes    map[*Img]*[2]*Shape // upright and turned a quarter
}

func NewPacker(cell, gap int) *Packer {
	return &Packer{cell: cell, gap: gap, shapes: map[*Img]*[2]*Shape{}}
}

func (p *Packer) shape(sprite *Img) *[2]*Shape {
	s := p.shapes[sprite]
	if s == nil {
		alpha := GrayAlpha(sprite)
		s = &[2]*Shape{newShape(alpha, p.cell, p.gap), newShape(RotateGray90(alpha, 1), p.cell, p.gap)}
		p.shapes[sprite] = s
	}
	return s
}

// Pack places sprites into free space of page; it returns the sprites that did not fit.
func (p *Packer) Pack(page *Img, occ []Bits, sprites []*Img) []*Img {
	cell, gap := p.cell, p.gap
	gridW := (page.Rect.Dx() + cell - 1) / cell
	gridH := len(occ)
	var left []*Img
	for _, sprite := range sprites {
		var bestX, bestY, bestTurn int
		found := false
		shapes := p.shape(sprite)
		for turn, s := range shapes {
			x, y, ok := FirstFit(occ, gridW, gridH, s)
			if !ok {
				continue
			}
			if !found || y < bestY || (y == bestY && x < bestX) {
				bestX, bestY, bestTurn, found = x, y, turn, true
			}
		}
		if !found {
			left = append(left, sprite)
			continue
		}
		delete(p.shapes, sprite)
		img := sprite
		if bestTurn == 1 {
			img = Rotate90(sprite, 1)
		}
		x, y := bestX+gap, bestY+gap
		PasteMask(page, img, x*cell, y*cell, GrayAlpha(img))
		for i, row := range shapes[bestTurn].Rows {
			orShifted(occ[y+i], row, -x)
		}
	}
	return left
}
