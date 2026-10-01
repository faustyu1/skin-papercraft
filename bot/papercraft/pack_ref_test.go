package papercraft

// Reference big.Int packer the bitset one must match.

import (
	"math/big"
)

// Free space is tracked on a coarse grid; each grid row is a bitmask, cell x is bit x.

// CellRows marks the grid cells that any non-zero mask pixel touches.
func refCellRows(mask *Gray, cell int) ([]*big.Int, int, int) {
	mw, mh := mask.Rect.Dx(), mask.Rect.Dy()
	w := (mw + cell - 1) / cell
	h := (mh + cell - 1) / cell
	rows := make([]*big.Int, h)
	for cy := 0; cy < h; cy++ {
		row := new(big.Int)
		for cx := 0; cx < w; cx++ {
			sum, n := 0, 0
			for y := 0; y < cell; y++ {
				yy := cy*cell + y
				if yy >= mh {
					break
				}
				for x := 0; x < cell; x++ {
					xx := cx*cell + x
					if xx >= mw {
						break
					}
					sum += int(mask.Pix[yy*mask.Stride+xx])
					n++
				}
			}
			if sum*2 >= n { // Image.reduce rounds; anything roundable to 1 is taken
				row.SetBit(row, cx, 1)
			}
		}
		rows[cy] = row
	}
	return rows, w, h
}

// Dilate grows a mask by gap cells on every side (it gets 2*gap bigger).
func refDilate(rows []*big.Int, w, h, gap int) ([]*big.Int, int, int) {
	grown := make([]*big.Int, 0, len(rows)+2*gap)
	for _, r := range rows {
		v := new(big.Int).Lsh(r, uint(gap))
		for i := 0; i < gap; i++ {
			t := new(big.Int)
			t.Lsh(v, 1)
			t.Or(t, new(big.Int).Rsh(v, 1))
			v.Or(v, t)
		}
		grown = append(grown, v)
	}
	for i := 0; i < gap; i++ {
		grown = append(grown, new(big.Int))
	}
	out := make([]*big.Int, len(grown))
	for i := range grown {
		v := new(big.Int)
		for j := max(0, i-gap); j < min(len(grown), i+gap+1); j++ {
			v.Or(v, grown[j])
		}
		out[i] = v
	}
	return out, w + 2*gap, h + 2*gap
}

// Run is a run of set bits: first bit and length.

func refRuns(row *big.Int) []Run {
	var out []Run
	bit, n := 0, row.BitLen()
	for bit < n {
		if row.Bit(bit) == 0 {
			bit++
			continue
		}
		start := bit
		for bit < n && row.Bit(bit) == 1 {
			bit++
		}
		out = append(out, Run{start, bit - start})
	}
	return out
}

// smear ORs v >> k for k in 0..n-1, in log(n) steps.
func refSmear(v *big.Int, n int) {
	for done := 1; done < n; {
		step := min(done, n-done)
		v.Or(v, new(big.Int).Rsh(v, uint(step)))
		done += step
	}
}

// FirstFit finds the top-most, then left-most spot where the piece overlaps nothing.
func refFirstFit(occ []*big.Int, gridW, gridH int, rows []*big.Int, w, h int) (int, int, bool) {
	if w > gridW || h > gridH {
		return 0, 0, false
	}
	xs := new(big.Int).Lsh(big.NewInt(1), uint(gridW-w+1))
	xs.Sub(xs, big.NewInt(1))

	type shapeRow struct {
		i    int
		runs []Run
	}
	var shape []shapeRow
	for i, r := range rows {
		if r.Sign() != 0 {
			shape = append(shape, shapeRow{i, refRuns(r)})
		}
	}
	for y := 0; y <= gridH-h; y++ {
		blocked := new(big.Int)
		for _, sr := range shape {
			o := occ[y+sr.i]
			for _, run := range sr.runs {
				t := new(big.Int).Rsh(o, uint(run.Bit))
				refSmear(t, run.Len)
				blocked.Or(blocked, t)
			}
			if blocked.Cmp(xs) == 0 {
				break
			}
		}
		free := new(big.Int).AndNot(xs, blocked)
		if free.Sign() != 0 {
			x := 0
			for free.Bit(x) == 0 {
				x++
			}
			return x, y, true
		}
	}
	return 0, 0, false
}

// Pack places sprites into free space of page; it returns the sprites that did not fit.
func refPack(page *Img, occ []*big.Int, sprites []*Img, cell, gap int) []*Img {
	gridW := (page.Rect.Dx() + cell - 1) / cell
	gridH := len(occ)
	var left []*Img
	for _, sprite := range sprites {
		var bestX, bestY int
		var bestImg *Img
		var bestRows []*big.Int
		found := false
		for _, img := range []*Img{sprite, Rotate90(sprite, 1)} {
			rows, w, h := refCellRows(GrayAlpha(img), cell)
			rows, w, h = refDilate(rows, w, h, gap)
			x, y, ok := refFirstFit(occ, gridW, gridH, rows, w, h)
			if !ok {
				continue
			}
			if !found || y < bestY || (y == bestY && x < bestX) {
				bestX, bestY, bestImg, bestRows, found = x, y, img, rows, true
			}
		}
		if !found {
			left = append(left, sprite)
			continue
		}
		x, y := bestX+gap, bestY+gap
		PasteMask(page, bestImg, x*cell, y*cell, GrayAlpha(bestImg))
		for i, row := range bestRows {
			occ[y+i].Or(occ[y+i], new(big.Int).Lsh(row, uint(x)))
		}
	}
	return left
}
