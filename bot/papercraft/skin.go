package papercraft

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"sort"
)

// Second-layer pixels fainter than this are not drawn (the game discards them too).
const ALPHACutoff = 26

// How far the second layer sticks out of the base box, in skin pixels (same as in game).
var inflateByPart = map[string]float64{"head": 0.5}

const inflateDefault = 0.25

type partDef struct {
	base, overlay [2]int
	size          [3]int
}

// Box UV origins in the skin texture: (u, v) of base layer and overlay layer.
var parts = map[string]partDef{
	"head":      {[2]int{0, 0}, [2]int{32, 0}, [3]int{8, 8, 8}},
	"body":      {[2]int{16, 16}, [2]int{16, 32}, [3]int{8, 12, 4}},
	"right_arm": {[2]int{40, 16}, [2]int{40, 32}, [3]int{4, 12, 4}},
	"left_arm":  {[2]int{32, 48}, [2]int{48, 48}, [3]int{4, 12, 4}},
	"right_leg": {[2]int{0, 16}, [2]int{0, 32}, [3]int{4, 12, 4}},
	"left_leg":  {[2]int{16, 48}, [2]int{0, 48}, [3]int{4, 12, 4}},
}

// LoadSkin decodes a PNG skin and downsizes HD skins to the standard grid.
func LoadSkin(data []byte) (*Img, error) {
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	skin := ToNRGBA(src)
	w, h := skin.Rect.Dx(), skin.Rect.Dy()
	if (w == 64 && h == 64) || (w == 64 && h == 32) {
		return skin, nil
	}
	if w%64 == 0 && (h == w || h == w/2) {
		nh := 32
		if h == w {
			nh = 64
		}
		return ResizeNearest(skin, 64, nh), nil
	}
	return nil, fmt.Errorf("unsupported skin size %dx%d", w, h)
}

// ToNRGBA converts any image to straight-alpha RGBA.
func ToNRGBA(src image.Image) *Img {
	if im, ok := src.(*Img); ok {
		return im
	}
	b := src.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			c := color.NRGBAModel.Convert(src.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
			i := y*out.Stride + x*4
			out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = c.R, c.G, c.B, c.A
		}
	}
	return out
}

// IsSlim reports whether the skin leaves the 4th column of the right arm's back empty.
func IsSlim(skin *Img) bool {
	if skin.Rect.Dy() != 64 {
		return false
	}
	for _, x := range []int{54, 55} {
		for y := 20; y < 32; y++ {
			if GetPixel(skin, x, y).A != 0 {
				return false
			}
		}
	}
	return true
}

// BoxFaces cuts the six faces of a box, each as seen from outside the model.
func BoxFaces(skin *Img, u, v, w, h, d int) map[string]*Img {
	crop := func(x, y, cw, ch int) *Img { return Crop(skin, x, y, x+cw, y+ch) }
	return map[string]*Img{
		"top":    crop(u+d, v, w, d),
		"bottom": FlipV(crop(u+d+w, v, w, d)),
		"right":  crop(u, v+d, d, h),
		"front":  crop(u+d, v+d, w, h),
		"left":   crop(u+d+w, v+d, d, h),
		"back":   crop(u+2*d+w, v+d, w, h),
	}
}

// MirrorFaces reuses the right limb, mirrored, for the left one (legacy 64x32 skins).
func MirrorFaces(faces map[string]*Img) map[string]*Img {
	return map[string]*Img{
		"top": FlipH(faces["top"]), "bottom": FlipH(faces["bottom"]),
		"front": FlipH(faces["front"]), "back": FlipH(faces["back"]),
		"right": FlipH(faces["left"]), "left": FlipH(faces["right"]),
	}
}

func opaqueFace(im *Img) *Img {
	out := Clone(im)
	PutAlpha(out, 255)
	return out
}

func partSize(name string, slim bool) [3]int {
	s := parts[name].size
	if slim && len(name) >= 4 && name[len(name)-3:] == "arm" {
		s[0] = 3
	}
	return s
}

// OverlayFaces returns the second-layer faces (hat, jacket, sleeves, pants), or nil if
// the layer is empty.
func OverlayFaces(skin *Img, name string, size [3]int) map[string]*Img {
	legacy := skin.Rect.Dy() == 32
	if legacy && name != "head" {
		return nil
	}
	p := parts[name]
	over := BoxFaces(skin, p.overlay[0], p.overlay[1], size[0], size[1], size[2])
	allOpaque, any := true, false
	for _, f := range over {
		lo, hi := GrayExtrema(GrayAlpha(f))
		if lo != 255 {
			allOpaque = false
		}
		if hi >= ALPHACutoff {
			any = true
		}
	}
	// Notch transparency hack: a fully opaque legacy hat is treated as empty.
	if legacy && allOpaque {
		return nil
	}
	if !any {
		return nil
	}
	return over
}

// PartFaces returns the base-layer faces of a part, optionally with the second layer
// painted on top.
func PartFaces(skin *Img, name string, slim, useOverlay bool) (map[string]*Img, [3]int) {
	size := partSize(name, slim)
	if skin.Rect.Dy() == 32 && len(name) > 5 && name[:5] == "left_" {
		mirrored, _ := PartFaces(skin, "right_"+name[5:], slim, useOverlay)
		return MirrorFaces(mirrored), size
	}
	p := parts[name]
	base := map[string]*Img{}
	for k, f := range BoxFaces(skin, p.base[0], p.base[1], size[0], size[1], size[2]) {
		base[k] = opaqueFace(f)
	}
	if useOverlay {
		if over := OverlayFaces(skin, name, size); over != nil {
			for k := range base {
				AlphaComposite(base[k], over[k])
			}
		}
	}
	return base, size
}

var partLayout = [][3]string{
	{"head", "rflb", "left"},
	{"body", "rflb", "left"},
	// Left column: character's right limbs (flap on the right), right column mirrored.
	{"right_arm", "brfl", "right"}, {"left_arm", "rflb", "left"},
	{"right_leg", "brfl", "right"}, {"left_leg", "rflb", "left"},
}

var pageRows = [][]string{{"head"}, {"body"}, {"right_arm", "left_arm"}, {"right_leg", "left_leg"}}

const gapUnits = 3

// PageGeometry returns the A4 page size and margin in pixels at the given dpi.
func PageGeometry(dpi int) (w, h, margin int) {
	w = int(math.Round(210 / 25.4 * float64(dpi)))
	h = int(math.Round(297 / 25.4 * float64(dpi)))
	margin = int(math.Round(10 / 25.4 * float64(dpi)))
	return
}

func rowHeight(row []*Net) float64 {
	m := 0.0
	for _, n := range row {
		m = math.Max(m, n.H)
	}
	return m
}

// BlankPage is a white A4 page with the credit in the corner, plus a mask of what is
// taken (255 = outside the printable area).
func BlankPage(dpi int, credit string, px int) (*Img, *Gray) {
	pageW, pageH, margin := PageGeometry(dpi)
	page := NewImg(pageW, pageH, color.NRGBA{255, 255, 255, 255})
	taken := image.NewGray(image.Rect(0, 0, pageW, pageH))
	FillGray(taken, 255)
	FillRectGray(taken, margin, margin, pageW-margin-1, pageH-margin-1, 0)
	if credit != "" {
		NewFont(math.Round(float64(px)*1.2)).Draw(page, float64(margin), float64(margin/2),
			credit, "lm", color.NRGBA{0, 0, 0, 255})
	}
	return page, taken
}

func basePage(nets map[string]*Net, px, dpi int, credit string) (*Img, *Gray) {
	pageW, _, margin := PageGeometry(dpi)
	page, taken := BlankPage(dpi, credit, px)
	lineW := max(2, px/12)
	y := margin
	for _, row := range pageRows {
		rowNets := make([]*Net, len(row))
		for i, name := range row {
			rowNets[i] = nets[name]
		}
		var xs []int
		if len(rowNets) == 1 {
			xs = []int{(pageW - int(rowNets[0].W)*px) / 2}
		} else {
			xs = []int{margin, pageW - margin - int(rowNets[1].W)*px}
		}
		for i, net := range rowNets {
			DrawNet(page, net, xs[i], y, px, lineW, taken)
		}
		y += int(rowHeight(rowNets))*px + gapUnits*px
	}
	return page, taken
}

// RenderSkin builds the pages of a skin papercraft, handing each page to emit as soon
// as it is done. layers: "separate" = second layer as cut-out pieces packed into free
// space, "flat" = painted onto the base, "none" = ignored.
func RenderSkin(skin *Img, pixelMM float64, dpi int, layers string, forceSlim *bool, credit string, emit func(*Img) error) (slim bool, log string, err error) {
	slim = forceSlim != nil && *forceSlim
	if forceSlim == nil {
		slim = IsSlim(skin)
	}

	base, shells := map[string]*Net{}, []*Net{}
	for _, pl := range partLayout {
		part, order, side := pl[0], pl[1], pl[2]
		faces, size := PartFaces(skin, part, slim, layers == "flat")
		base[part] = BuildNet(faces, [3]float64{float64(size[0]), float64(size[1]), float64(size[2])}, order, side)
		if layers == "separate" {
			if over := OverlayFaces(skin, part, size); over != nil {
				inflate := inflateDefault
				if v, ok := inflateByPart[part]; ok {
					inflate = v
				}
				shells = append(shells, BuildShellNet(over, size, inflate, order))
			}
		}
	}

	_, pageH, margin := PageGeometry(dpi)
	px := int(math.Round(pixelMM / 25.4 * float64(dpi)))
	var rows [][]*Net
	for _, row := range pageRows {
		var r []*Net
		for _, name := range row {
			r = append(r, base[name])
		}
		rows = append(rows, r)
	}
	total := 0.0
	for _, r := range rows {
		total += rowHeight(r)
	}
	maxPx := int((float64(pageH - 2*margin)) / (total + float64(gapUnits*(len(rows)-1))))
	if px > maxPx {
		log = fmt.Sprintf("  pixel size reduced to %.2f mm to fit A4\n", float64(maxPx)/float64(dpi)*25.4)
		px = maxPx
	}
	lineW := max(2, px/12)

	page, taken := basePage(base, px, dpi, credit)
	var sprites []*Img
	for _, net := range shells {
		sprites = append(sprites, ShellPieces(net, px, lineW)...)
	}
	sort.SliceStable(sprites, func(i, j int) bool {
		return sprites[i].Rect.Dx()*sprites[i].Rect.Dy() > sprites[j].Rect.Dx()*sprites[j].Rect.Dy()
	})

	cell := max(4, int(math.Round(float64(dpi)/25.4)))
	gap := 2
	packer := NewPacker(cell, gap)
	first := true
	for {
		occ, _, _ := CellRows(taken, cell)
		before := len(sprites)
		sprites = packer.Pack(page, occ, sprites)
		if len(sprites) == 0 {
			return slim, log, emit(page)
		}
		if len(sprites) == before && !first {
			return slim, log, errors.New("a second-layer piece is bigger than a whole page")
		}
		if err := emit(page); err != nil {
			return slim, log, err
		}
		page, taken = BlankPage(dpi, credit, px)
		first = false
	}
}
