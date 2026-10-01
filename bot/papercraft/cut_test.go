package papercraft

import (
	"image"
	"image/color"
	"math"
	"testing"
)

func cube(name string, from, to V3) *Element {
	e := &Element{Name: name, From: from, To: to, Faces: map[string]*Face{}}
	return e
}

func partsVolume(e *Element) float64 {
	v := solidVolume(e.Solid)
	for _, p := range e.Extra {
		v += solidVolume(p)
	}
	return v
}

func TestSubtractIsExact(t *testing.T) {
	a, b := cube("a", V3{0, 0, 0}, V3{2, 2, 2}), cube("b", V3{1, 1, 1}, V3{3, 3, 3})
	b.Solid = boxSolid(b)
	parts := subtract(boxSolid(a), partPlanes(a, b, b.Solid), solidVolume)
	total := 0.0
	for _, p := range parts {
		total += solidVolume(p.S)
	}
	if math.Abs(total-7) > 1e-9 {
		t.Fatalf("a minus the corner b covers is %g, want 7 (%d parts)", total, len(parts))
	}
}

// A hand resting on the corner of a belly: one flat cut would leave a hole where the
// hand sticks out past the belly; the hand must come out in parts with nothing missing.
func TestResolveOverlapsLeavesNoHole(t *testing.T) {
	belly := cube("belly", V3{0, 0, 0}, V3{20, 16, 20})
	hand := cube("hand", V3{16, 4, -3}, V3{22, 10, 4})
	buried := resolveOverlaps([]*Element{belly, hand}, 0.05)
	if len(buried) != 0 {
		t.Fatalf("buried %v", buried[0].Name)
	}
	if partsVolume(belly) != 20*16*20 {
		t.Fatalf("the belly was cut: %g", partsVolume(belly))
	}
	// The hand keeps everything outside the belly: 6·6·7 minus the 4·6·4 inside it.
	if got, want := partsVolume(hand), 6.0*6*7-4*6*4; math.Abs(got-want) > 1e-6 {
		t.Fatalf("hand keeps %g, want %g", got, want)
	}
	if len(hand.Extra) == 0 {
		t.Fatal("hand was not split around the corner")
	}
}

// A flat cube pierced in the middle keeps both sides of it.
func TestPlaneKeepsBothSidesOfAPiercingCube(t *testing.T) {
	plane := cube("plane", V3{-10, 0, 0}, V3{10, 6, 0})
	post := cube("post", V3{-1, -2, -1}, V3{1, 8, 1})
	resolveOverlaps([]*Element{plane, post}, 0.05)
	if len(plane.PlaneCuts) != 2 {
		t.Fatalf("plane parts: %d", len(plane.PlaneCuts))
	}
	area := 0.0
	for _, c := range plane.PlaneCuts {
		area += PolyArea(c.Poly, V3{0, 0, 1})
		if !c.HasGlue {
			t.Fatal("part has no glue edge")
		}
	}
	if math.Abs(area-18*6) > 1e-6 {
		t.Fatalf("plane keeps %g, want %g", area, 18.0*6)
	}
}

// A plane painted on its front only is printed with the mirrored front as its back.
func TestOneSidedPlaneGetsABack(t *testing.T) {
	tex := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for x := 0; x < 8; x++ {
		for y := 0; y < 4; y++ {
			c := color.NRGBA{uint8(30 * x), 0, 0, 255}
			tex.SetNRGBA(x, y, c)
		}
	}
	zero := 0
	plane := cube("p", V3{0, 0, 0}, V3{8, 4, 0})
	plane.Faces["north"] = &Face{Texture: &zero, UV: []float64{0, 0, 8, 4}}
	plane.Faces["south"] = &Face{Texture: &zero, UV: []float64{8, 0, 16, 4}} // blank
	textures := []*Texture{{Img: tex, ScaleX: 1, ScaleY: 1}}
	pieces := planePiece(plane, textures, 2, 4, "", 2)
	if len(pieces) != 1 {
		t.Fatalf("pieces: %d", len(pieces))
	}
	// Front and back side by side, each 8 units · 2 mm · 4 px wide.
	if w := pieces[0].Rect.Dx(); w < 2*8*2*4 {
		t.Fatalf("no back printed: %d px wide", w)
	}
}
