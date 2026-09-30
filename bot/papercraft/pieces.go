package papercraft

import (
	"fmt"
	"image"
	"image/color"
	"math"
)

// Box net faces in skin_papercraft terms: the ring goes east, north, west, south as seen
// from outside, the lid and the bottom hang off the north face turned by 180 degrees.
var boxFaceNames = map[string]string{
	"right": "east", "front": "north", "left": "west", "back": "south", "top": "up", "bottom": "down",
}

var faceOrder = []string{"north", "south", "east", "west", "up", "down"}

// Flat cubes: which two faces form the piece and whether they sit side by side.
type planeFaces struct {
	first, second string
	sideBySide    bool
}

var planeFaceNames = map[int]planeFaces{
	0: {"east", "west", true},
	1: {"up", "down", false},
	2: {"north", "south", true},
}

var faceWords = map[string]string{"north": "перед", "south": "зад", "east": "право", "west": "лево", "up": "верх", "down": "низ"}

var (
	black = color.NRGBA{0, 0, 0, 255}
	white = color.NRGBA{255, 255, 255, 255}
	gray  = color.NRGBA{90, 90, 90, 255}
)

// Sheet is the drawing context of one sprite, in millimetres.
type Sheet struct {
	k     float64
	lineW int
	dash  int
	font  *Font
	img   *Img
	ox    float64
	oy    float64
}

func newSheet(lo, hi PointF, pxMM float64, label string) *Sheet {
	s := &Sheet{k: pxMM}
	s.lineW = max(2, int(math.Round(0.2*pxMM)))
	s.dash = max(2, int(math.Round(0.6*pxMM)))
	s.font = NewFont(math.Round(2.8 * pxMM))
	pad := s.lineW * 2
	labelH := int(math.Round(3.6 * pxMM))
	s.ox, s.oy = float64(pad)-lo.X*pxMM, float64(pad+labelH)-lo.Y*pxMM
	w := int(math.Round((hi.X-lo.X)*pxMM)) + 2*pad
	h := int(math.Round((hi.Y-lo.Y)*pxMM)) + 2*pad + labelH
	s.img = NewImg(w, h, color.NRGBA{})
	if label != "" {
		s.font.Draw(s.img, float64(pad), float64(labelH/2), label, "lm", black)
	}
	return s
}

func (s *Sheet) pt(p PointF) PointF {
	return PointF{s.ox + p.X*s.k, s.oy + p.Y*s.k}
}

func (s *Sheet) rect(x, y, w, h float64) (int, int, int, int) {
	a, b := s.pt(PointF{x, y}), s.pt(PointF{x + w, y + h})
	return int(math.Round(a.X)), int(math.Round(a.Y)), int(math.Round(b.X)), int(math.Round(b.Y))
}

func (s *Sheet) face(img *Img, x, y, w, h float64) {
	X0, Y0, X1, Y1 := s.rect(x, y, w, h)
	tile := NewImg(max(1, X1-X0), max(1, Y1-Y0), white)
	if img != nil {
		AlphaComposite(tile, ResizeNearest(img, tile.Rect.Dx(), tile.Rect.Dy()))
	}
	Paste(s.img, tile, X0, Y0)
}

func (s *Sheet) tab(poly []PointF) {
	pts := make([]PointF, len(poly))
	for i, p := range poly {
		pts[i] = s.pt(p)
	}
	Polygon(s.img, pts, white)
	Line(s.img, pts, black, float64(s.lineW), true)
}

func (s *Sheet) fold(p0, p1 PointF) {
	DashedLine(s.img, s.pt(p0), s.pt(p1), float64(max(1, s.lineW/2)), float64(s.dash), gray)
}

func (s *Sheet) cut(p0, p1 PointF) {
	Line(s.img, []PointF{s.pt(p0), s.pt(p1)}, black, float64(s.lineW), false)
}

func bounds(pts []PointF) (PointF, PointF) {
	lo := PointF{math.Inf(1), math.Inf(1)}
	hi := PointF{math.Inf(-1), math.Inf(-1)}
	for _, p := range pts {
		lo.X, lo.Y = math.Min(lo.X, p.X), math.Min(lo.Y, p.Y)
		hi.X, hi.Y = math.Max(hi.X, p.X), math.Max(hi.Y, p.Y)
	}
	return lo, hi
}

func boxPiece(e *Element, textures []*Texture, unit, pxMM float64, label string) *Img {
	lo, hi := e.box()
	size := [3]float64{(hi[0] - lo[0]) * unit, (hi[1] - lo[1]) * unit, (hi[2] - lo[2]) * unit}
	layout := BuildNetLayout(size, "rflb", "left")
	dims := e.faceDims()

	points := []PointF{{0, 0}, {layout.W, layout.H}}
	for _, tab := range layout.Tabs {
		points = append(points, tab...)
	}
	blo, bhi := bounds(points)
	sheet := newSheet(blo, bhi, pxMM, label)
	for _, tab := range layout.Tabs {
		sheet.tab(tab)
	}
	for _, p := range layout.Placed {
		name := boxFaceNames[p.Name]
		d := dims[name]
		w, h := d[0]*unit, d[1]*unit
		img := sample(textures, e.Faces[name], w, h, pxMM, name == "up" || name == "down")
		sheet.face(img, p.X0, p.Y0, p.X1-p.X0, p.Y1-p.Y0)
	}
	for _, s := range layout.Fold {
		sheet.fold(s[0], s[1])
	}
	for _, s := range layout.Cut {
		sheet.cut(s[0], s[1])
	}
	return sheet.img
}

// OverlayPieces prints an outer layer (hat, jacket, sleeves) as one cut-out per face,
// glued flat over the matching face of the cube inside, like the second layer of a skin.
func overlayPieces(e *Element, textures []*Texture, unit, pxMM float64, label string) []*Img {
	var pieces []*Img
	dims := e.faceDims()
	for _, name := range faceOrder {
		d := dims[name]
		w, h := d[0]*unit, d[1]*unit
		img := sample(textures, e.Faces[name], w, h, pxMM, false)
		if img == nil {
			continue
		}
		if _, hi := GrayExtrema(GrayAlpha(img)); hi < ALPHACutoff {
			continue
		}
		pieces = append(pieces, cutoutPiece(img, w, h, pxMM, fmt.Sprintf("%s %s", label, faceWords[name]), nil, false))
	}
	return pieces
}

type cutSeg struct {
	P0, P1 PointF
	C      PointF
}

func planePiece(e *Element, textures []*Texture, unit, pxMM float64, label string, axis int) []*Img {
	pf := planeFaceNames[axis]
	dims := e.faceDims()
	w, h := dims[pf.first][0]*unit, dims[pf.first][1]*unit
	type kept struct {
		img  *Img
		name string
	}
	var keep []kept
	for _, n := range []string{pf.first, pf.second} {
		img := sample(textures, e.Faces[n], w, h, pxMM, false)
		if img == nil {
			continue
		}
		if _, hi := GrayExtrema(GrayAlpha(img)); hi >= ALPHACutoff {
			keep = append(keep, kept{img, n})
		}
	}
	if len(keep) == 0 {
		return nil
	}
	corners := e.faceCorners()
	if e.PlaneCut != nil {
		// Part of the plane sinks into a solid piece: print only what stays outside and
		// glue it on along the cut line.
		poly3d := e.PlaneCut.Poly
		var pieces []*Img
		for _, k := range keep {
			tl, tr, bl := corners[k.name][0], corners[k.name][1], corners[k.name][2]
			ux, vy := vSub(tr, tl), vSub(bl, tl)
			st := func(q V3) (float64, float64) {
				d := vSub(q, tl)
				return vDot(d, ux) / vDot(ux, ux), vDot(d, vy) / vDot(vy, vy)
			}
			W, H := k.img.Rect.Dx(), k.img.Rect.Dy()
			mask := NewGray(k.img.Bounds())
			pts := make([]PointF, 0, len(poly3d))
			for _, q := range poly3d {
				a, b := st(q)
				pts = append(pts, PointF{a * float64(W), b * float64(H)})
			}
			PolygonGray(mask, pts, 255)
			img := Clone(k.img)
			alpha := GrayAlpha(img)
			mult := GrayMultiply(alpha, mask)
			for i := range img.Pix {
				if i%4 == 3 {
					img.Pix[i] = mult.Pix[i/4]
				}
			}
			var seg *cutSeg
			if e.PlaneCut.HasGlue {
				a0, b0 := st(e.PlaneCut.Glue[0])
				a1, b1 := st(e.PlaneCut.Glue[1])
				var cx, cy float64
				for _, q := range poly3d {
					a, b := st(q)
					cx, cy = cx+a, cy+b
				}
				cx, cy = cx/float64(len(poly3d)), cy/float64(len(poly3d))
				seg = &cutSeg{PointF{a0 * w, b0 * h}, PointF{a1 * w, b1 * h}, PointF{cx * w, cy * h}}
			}
			pieces = append(pieces, cutoutPiece(img, w, h, pxMM, label, seg, true))
		}
		return pieces
	}
	through := false
	for _, k := range keep {
		if seeThrough(k.img) {
			through = true
			break
		}
	}
	if through {
		var pieces []*Img
		for _, k := range keep {
			pieces = append(pieces, cutoutPiece(k.img, w, h, pxMM, label, nil, true))
		}
		return pieces
	}

	// Fold-over piece: the second face hangs off the first one, fold and glue back to back.
	dx, dy := 0.0, 0.0
	if pf.sideBySide {
		dx = w
	} else {
		dy = h
	}
	W := w + dx*float64(len(keep)-1)
	H := h + dy*float64(len(keep)-1)
	sheet := newSheet(PointF{0, 0}, PointF{W, H}, pxMM, label)
	for i, k := range keep {
		sheet.face(k.img, float64(i)*dx, float64(i)*dy, w, h)
	}
	for _, s := range []Seg{{{0, 0}, {W, 0}}, {{W, 0}, {W, H}}, {{W, H}, {0, H}}, {{0, H}, {0, 0}}} {
		sheet.cut(s[0], s[1])
	}
	if len(keep) == 2 {
		sheet.fold(PointF{dx, dy}, PointF{w, h})
	}
	return []*Img{sheet.img}
}

// FlapPoly is a glue flap as long as the whole edge, with slanted ends.
func FlapPoly(p0, p1, normal PointF, depth float64) []PointF {
	length := math.Hypot(p1.X-p0.X, p1.Y-p0.Y)
	dx, dy := (p1.X-p0.X)/length, (p1.Y-p0.Y)/length
	inset := math.Min(depth, length/4)
	nx, ny := normal.X, normal.Y
	return []PointF{
		p0,
		{p0.X + dx*inset + nx*depth, p0.Y + dy*inset + ny*depth},
		{p1.X - dx*inset + nx*depth, p1.Y - dy*inset + ny*depth},
		p1,
	}
}

// CutoutPiece cuts a see-through plane along its shape, with a glue flap on the edge
// that most of the shape touches (hair, fins, antennae grow from that edge), or on the
// cut line `seg` when the piece was cut off along a line.
func cutoutPiece(img *Img, w, h, pxMM float64, label string, seg *cutSeg, flap bool) *Img {
	W, H := img.Rect.Dx(), img.Rect.Dy()
	alpha := GrayAlpha(img)
	for i, v := range alpha.Pix {
		if v >= ALPHACutoff {
			alpha.Pix[i] = 255
		} else {
			alpha.Pix[i] = 0
		}
	}
	type edge struct {
		name  string
		strip *Gray
		p0    PointF
		p1    PointF
		n     PointF
	}
	edges := []edge{
		{"top", CropGray(alpha, 0, 0, W, 1), PointF{0, 0}, PointF{w, 0}, PointF{0, -1}},
		{"bottom", CropGray(alpha, 0, H-1, W, H), PointF{w, h}, PointF{0, h}, PointF{0, 1}},
		{"left", CropGray(alpha, 0, 0, 1, H), PointF{0, h}, PointF{0, 0}, PointF{-1, 0}},
		{"right", CropGray(alpha, W-1, 0, W, H), PointF{w, 0}, PointF{w, h}, PointF{1, 0}},
	}
	best := edges[0]
	bestTouch := -1.0
	for _, e := range edges {
		sum := 0
		for _, v := range e.strip.Pix {
			sum += int(v)
		}
		touch := float64(sum) / 255 / float64(len(e.strip.Pix))
		if touch > bestTouch {
			best, bestTouch = e, touch
		}
	}

	var glue, fold []PointF
	if seg != nil && math.Hypot(seg.P1.X-seg.P0.X, seg.P1.Y-seg.P0.Y) > 0.5 {
		p0, p1, c := seg.P0, seg.P1, seg.C
		length := math.Hypot(p1.X-p0.X, p1.Y-p0.Y)
		nx, ny := (p1.Y-p0.Y)/length, -(p1.X-p0.X)/length
		if nx*(p0.X-c.X)+ny*(p0.Y-c.Y) < 0 {
			nx, ny = -nx, -ny
		}
		glue, fold = FlapPoly(p0, p1, PointF{nx, ny}, 3), []PointF{p0, p1}
	} else if flap && bestTouch > 0 {
		if grayBBox(alpha) != [4]int{0, 0, W, H} {
			glue, fold = FlapPoly(best.p0, best.p1, best.n, 3), []PointF{best.p0, best.p1}
		}
	}

	points := []PointF{{0, 0}, {w, h}}
	points = append(points, glue...)
	blo, bhi := bounds(points)
	sheet := newSheet(blo, bhi, pxMM, label)
	X0, Y0, X1, Y1 := sheet.rect(0, 0, w, h)
	body := NewImg(X1-X0, Y1-Y0, white)
	AlphaComposite(body, ResizeNearest(img, body.Rect.Dx(), body.Rect.Dy()))
	mask := NewGray(sheet.img.Bounds())
	PasteGray(mask, ResizeNearestGray(alpha, X1-X0, Y1-Y0), X0, Y0)
	if glue != nil {
		pts := make([]PointF, len(glue))
		for i, p := range glue {
			pts[i] = sheet.pt(p)
		}
		PolygonGray(mask, pts, 255)
	}

	ring := GraySubtract(MaxFilter(mask, sheet.lineW), mask)
	paper := NewImg(sheet.img.Rect.Dx(), sheet.img.Rect.Dy(), white)
	Paste(paper, body, X0, Y0)
	labelImg := sheet.img
	sheet.img = NewImg(labelImg.Rect.Dx(), labelImg.Rect.Dy(), color.NRGBA{})
	PasteMask(sheet.img, paper, 0, 0, mask)
	PasteMask(sheet.img, NewImg(labelImg.Rect.Dx(), labelImg.Rect.Dy(), black), 0, 0, ring)
	AlphaComposite(sheet.img, labelImg)
	if glue != nil {
		sheet.fold(fold[0], fold[1])
	}
	return sheet.img
}

// CropGray cuts a grayscale region.
func CropGray(im *Gray, x0, y0, x1, y1 int) *Gray {
	r := image.Rect(x0, y0, x1, y1).Intersect(im.Bounds())
	out := image.NewGray(image.Rect(0, 0, r.Dx(), r.Dy()))
	for y := 0; y < r.Dy(); y++ {
		copy(out.Pix[y*out.Stride:(y+1)*out.Stride], im.Pix[(r.Min.Y+y)*im.Stride+r.Min.X:])
	}
	return out
}

// ResizeNearestGray scales a mask with nearest-neighbour sampling.
func ResizeNearestGray(im *Gray, w, h int) *Gray {
	out := NewGray(imageRect(w, h))
	sw, sh := im.Rect.Dx(), im.Rect.Dy()
	for y := 0; y < h; y++ {
		sy := clampInt(int((float64(y)+0.5)*float64(sh)/float64(h)), 0, sh-1)
		for x := 0; x < w; x++ {
			sx := clampInt(int((float64(x)+0.5)*float64(sw)/float64(w)), 0, sw-1)
			out.Pix[y*out.Stride+x] = im.Pix[sy*im.Stride+sx]
		}
	}
	return out
}

// PasteGray copies a mask region.
func PasteGray(dst, src *Gray, x, y int) {
	sb := src.Bounds()
	db := dst.Bounds()
	for sy := 0; sy < sb.Dy(); sy++ {
		dy := y + sy
		if dy < db.Min.Y || dy >= db.Max.Y {
			continue
		}
		for sx := 0; sx < sb.Dx(); sx++ {
			dx := x + sx
			if dx < db.Min.X || dx >= db.Max.X {
				continue
			}
			dst.Pix[dy*dst.Stride+dx] = src.Pix[sy*src.Stride+sx]
		}
	}
}

func grayBBox(im *Gray) [4]int {
	w, h := im.Rect.Dx(), im.Rect.Dy()
	x0, y0, x1, y1 := w, h, -1, -1
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if im.Pix[y*im.Stride+x] != 0 {
				x0, y0 = min(x0, x), min(y0, y)
				x1, y1 = max(x1, x), max(y1, y)
			}
		}
	}
	return [4]int{x0, y0, x1 + 1, y1 + 1}
}

// ElementPieces returns the printable sprites of one element.
func ElementPieces(e *Element, textures []*Texture, unit, pxMM float64, label string) []*Img {
	if e.Over != nil {
		return overlayPieces(e, textures, unit, pxMM, label)
	}
	lo, hi := e.box()
	var flat []int
	for i := range 3 {
		if (hi[i]-lo[i])*unit < thinMM {
			flat = append(flat, i)
		}
	}
	if len(flat) > 1 {
		return nil
	}
	if len(flat) == 1 {
		return planePiece(e, textures, unit, pxMM, label, flat[0])
	}
	if e.Cut != nil {
		if pieces := slantedPiece(e, *e.Cut, textures, unit, pxMM, label); pieces != nil {
			return pieces
		}
	}
	return []*Img{boxPiece(e, textures, unit, pxMM, label)}
}
