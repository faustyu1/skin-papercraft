package papercraft

import (
	"image"
	"math/big"
	"math/rand"
	"testing"
)

func toBig(b Bits) *big.Int {
	v := new(big.Int)
	for i := range len(b) * 64 {
		if b.bit(i) {
			v.SetBit(v, i, 1)
		}
	}
	return v
}

func randSprite(r *rand.Rand) *Img {
	w, h := 5+r.Intn(300), 5+r.Intn(300)
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	// A blob with holes so rows have several runs.
	for y := range h {
		for x := range w {
			if (x-w/2)*(x-w/2)*h*h+(y-h/2)*(y-h/2)*w*w < w*w*h*h/4 && r.Intn(50) != 0 && (x/20+y/30)%3 != 0 {
				im.Pix[y*im.Stride+x*4+3] = 255
				im.Pix[y*im.Stride+x*4] = uint8(x)
			}
		}
	}
	return im
}

func TestPackMatchesReference(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	var sprites []*Img
	for range 120 {
		sprites = append(sprites, randSprite(r))
	}
	cell, gap := 12, 2
	refLeft, left := sprites, sprites
	packer := NewPacker(cell, gap)
	for page := 0; len(left) > 0; page++ {
		refPage, taken := BlankPage(300, "test", 30)
		newPage := Clone(refPage)
		refOcc, _, _ := refCellRows(taken, cell)
		occ, _, _ := CellRows(taken, cell)
		for i := range occ {
			if toBig(occ[i]).Cmp(refOcc[i]) != 0 {
				t.Fatalf("page %d: CellRows row %d differs", page, i)
			}
		}
		refLeft = refPack(refPage, refOcc, refLeft, cell, gap)
		left = packer.Pack(newPage, occ, left)
		if len(refLeft) != len(left) {
			t.Fatalf("page %d: %d sprites left, reference %d", page, len(left), len(refLeft))
		}
		for i := range occ {
			if toBig(occ[i]).Cmp(refOcc[i]) != 0 {
				t.Fatalf("page %d: occupancy row %d differs", page, i)
			}
		}
		if string(newPage.Pix) != string(refPage.Pix) {
			t.Fatalf("page %d differs", page)
		}
	}
}

func TestRotateGray90MatchesRotate90(t *testing.T) {
	im := randSprite(rand.New(rand.NewSource(3)))
	for n := range 4 {
		got, want := RotateGray90(GrayAlpha(im), n), GrayAlpha(Rotate90(im, n))
		if got.Rect != want.Rect || string(got.Pix) != string(want.Pix) {
			t.Fatalf("turn %d differs", n)
		}
	}
}
