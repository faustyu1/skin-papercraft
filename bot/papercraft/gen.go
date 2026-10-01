package papercraft

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

func netExtent(e *Element) [2]float64 {
	s := e.size()
	w, h, d := s[0], s[1], s[2]
	var flat []int
	for i, n := range s {
		if n < eps {
			flat = append(flat, i)
		}
	}
	if len(flat) == 0 {
		return [2]float64{2 * (w + d), h + 2*d}
	}
	if flat[0] == 1 {
		return [2]float64{w, 2 * d}
	}
	if flat[0] == 2 {
		return [2]float64{2 * w, h}
	}
	return [2]float64{2 * d, h}
}

func fitUnit(extent, limit [2]float64) float64 {
	a, b := math.Max(extent[0], eps), math.Max(extent[1], eps)
	lx, ly := limit[0]-pieceExtraMM, limit[1]-pieceExtraMM
	return math.Max(math.Min(lx/a, ly/b), math.Min(lx/b, ly/a))
}

func spriteArea(s *Img) int { return s.Rect.Dx() * s.Rect.Dy() }

// RenderModel turns a Blockbench model into papercraft pages. Every page is handed to
// emit as soon as it is done. Big models take a while: every pair of cubes that sink
// into each other is cut apart first.
func RenderModel(data []byte, unitMM float64, dpi int, credit string, emit func(*Img) error) (float64, string, error) {
	var log strings.Builder
	elements, textures, err := LoadModel(data)
	if err != nil {
		return 0, log.String(), err
	}
	var kept []*Element
	for _, e := range elements {
		switch {
		case watermark.MatchString(e.Name):
			fmt.Fprintf(&log, "  skipped watermark %q\n", e.Name)
		case e.Visibility != nil && !*e.Visibility, e.Export != nil && !*e.Export:
		default:
			kept = append(kept, e)
		}
	}
	clear := map[*Element]bool{}
	for _, e := range kept {
		if clearCube(e, textures) {
			clear[e] = true
		}
	}
	var visible []*Element
	for _, e := range kept {
		if !HiddenInside(e, kept, clear) {
			visible = append(visible, e)
		}
	}
	if n := len(kept) - len(visible); n > 0 {
		fmt.Fprintf(&log, "  skipped %d cube(s) hidden inside others\n", n)
	}

	pageW, pageH, margin := PageGeometry(dpi)
	pxMM := float64(dpi) / 25.4
	// Shrink the model until its biggest piece fits on a page: first from the net sizes
	// (drawing a piece at a wrong scale can take metres of pixels), then exactly.
	limit := [2]float64{
		(float64(pageW) - 2*float64(margin)) / pxMM,
		(float64(pageH)-2*float64(margin))/pxMM - 6,
	}
	fit := math.Inf(1)
	for _, e := range visible {
		fit = math.Min(fit, fitUnit(netExtent(e), limit))
	}
	if fit < unitMM {
		unitMM = math.Max(0.05, math.Trunc(fit*20)/20)
		fmt.Fprintf(&log, "  unit reduced to %g mm to fit A4\n", unitMM)
	}
	tiny := 0
	for _, e := range visible {
		best := 2.0
		for _, d := range e.size() {
			if v := d * unitMM; v >= thinMM && v < best {
				best = v
			}
		}
		if best < 2 {
			tiny++
		}
	}
	if tiny > 0 {
		hint := "try a bigger --unit-mm"
		if fit < unitMM+0.05 {
			hint = "the biggest piece limits the scale, the model is too detailed for A4"
		}
		fmt.Fprintf(&log, "  %d piece(s) have edges under 2 mm, hard to cut: %s\n", tiny, hint)
	}
	// See-through cubes around other cubes are outer layers: printed as cut-outs glued
	// over the cube inside, and left out of cutting cubes apart.
	for _, e := range visible {
		if clear[e] {
			e.Over = e.wrapped(visible, 0.05)
		}
	}

	whole := visible
	var sprites []*Img
	numbers := map[*Element]int{}
	for {
		var solidElems []*Element
		for _, e := range whole {
			if e.Over == nil {
				solidElems = append(solidElems, e)
			}
		}
		// Cubes thinner than paper become planes, which depends on the scale.
		buried := resolveOverlaps(solidElems, thinMM/unitMM)
		visible = nil
		for _, e := range whole {
			isBuried := false
			for _, b := range buried {
				if e == b {
					isBuried = true
					break
				}
			}
			if !isBuried {
				visible = append(visible, e)
			}
		}
		numbers = map[*Element]int{}
		for i, e := range visible {
			numbers[e] = i + 1
		}
		sprites = nil
		for _, e := range visible {
			sprites = append(sprites, ElementPieces(e, textures, unitMM, pxMM, fmt.Sprint(numbers[e]))...)
		}
		if len(sprites) == 0 {
			return 0, log.String(), errors.New("model has no printable pieces")
		}
		worst := math.Inf(-1)
		for _, s := range sprites {
			w := math.Min(
				math.Max(float64(s.Rect.Dx())/pxMM/limit[0], float64(s.Rect.Dy())/pxMM/limit[1]),
				math.Max(float64(s.Rect.Dy())/pxMM/limit[0], float64(s.Rect.Dx())/pxMM/limit[1]),
			)
			worst = math.Max(worst, w)
		}
		if worst <= 1 {
			if n := len(buried); n > 0 {
				fmt.Fprintf(&log, "  skipped %d cube(s) buried inside others\n", n)
			}
			break
		}
		unitMM = math.Trunc(unitMM/worst*20) / 20
		fmt.Fprintf(&log, "  unit reduced to %g mm to fit A4\n", unitMM)
	}
	sort.SliceStable(sprites, func(i, j int) bool { return spriteArea(sprites[i]) > spriteArea(sprites[j]) })

	cell := max(4, int(math.Round(pxMM))) // ~1 mm grid
	gap := 2
	creditPx := int(math.Round(2.5 * pxMM))
	packer := NewPacker(cell, gap)
	for len(sprites) > 0 {
		page, taken := BlankPage(dpi, credit, creditPx)
		occ, _, _ := CellRows(taken, cell)
		before := len(sprites)
		sprites = packer.Pack(page, occ, sprites)
		if len(sprites) == before {
			return 0, log.String(), errors.New("a piece is bigger than a whole page")
		}
		if err := emit(page); err != nil {
			return 0, log.String(), err
		}
	}
	for _, page := range legendPage(visible, numbers, textures, dpi, credit) {
		if err := emit(page); err != nil {
			return 0, log.String(), err
		}
	}
	return unitMM, log.String(), nil
}

// ModelPages is a finished model papercraft.
type ModelPages struct {
	Pages []*Img
	Unit  float64
	Log   string
}

// GenerateModel builds all pages of a Blockbench model papercraft.
func GenerateModel(data []byte, unitMM float64, dpi int, credit string) (*ModelPages, error) {
	var pages []*Img
	unit, log, err := RenderModel(data, unitMM, dpi, credit, func(p *Img) error {
		pages = append(pages, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &ModelPages{Pages: pages, Unit: unit, Log: log}, nil
}

// SkinPages is a finished skin papercraft.
type SkinPages struct {
	Pages []*Img
	Slim  bool
	Log   string
}

// GenerateSkin builds all pages of a skin papercraft. layers is separate/flat/none and
// model is auto/steve/alex.
func GenerateSkin(data []byte, pixelMM float64, dpi int, layers, model, credit string) (*SkinPages, error) {
	skin, err := LoadSkin(data)
	if err != nil {
		return nil, err
	}
	var forceSlim *bool
	switch model {
	case "steve":
		v := false
		forceSlim = &v
	case "alex":
		v := true
		forceSlim = &v
	}
	var pages []*Img
	slim, log, err := RenderSkin(skin, pixelMM, dpi, layers, forceSlim, credit, func(p *Img) error {
		pages = append(pages, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &SkinPages{Pages: pages, Slim: slim, Log: log}, nil
}
