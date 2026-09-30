package papercraft

import (
	"image/color"
	"math"
)

// Net is a box net in skin-pixel units.
type Net struct {
	Faces []PlacedFace
	Tabs  [][]PointF
	Cut   []Seg
	Fold  []Seg
	W, H  float64
}

// PlacedFace is a face texture and the box it occupies in net coordinates.
type PlacedFace struct {
	Img    *Img
	X0, Y0 float64
	X1, Y1 float64
}

type Seg [2]PointF

var RowOrders = map[string][]string{
	// Faces go around the box in this order when seen from outside.
	"rflb": {"right", "front", "left", "back"},
	"brfl": {"back", "right", "front", "left"},
}

// Flap is the glue flap on edge p0-p1: triangle on short edges, trapezoid on long ones.
func Flap(p0, p1 PointF, normal [2]float64, depth float64) []PointF {
	length := math.Abs(p1.X-p0.X) + math.Abs(p1.Y-p0.Y)
	dx, dy := (p1.X-p0.X)/length, (p1.Y-p0.Y)/length
	nx, ny := normal[0], normal[1]
	if length <= 4 {
		mx, my := (p0.X+p1.X)/2, (p0.Y+p1.Y)/2
		return []PointF{p0, {mx + nx*depth, my + ny*depth}, p1}
	}
	inset := math.Min(depth, length/4)
	return []PointF{
		p0,
		{p0.X + dx*inset + nx*depth, p0.Y + dy*inset + ny*depth},
		{p1.X - dx*inset + nx*depth, p1.Y - dy*inset + ny*depth},
		p1,
	}
}

// NetLayout is the geometry of a box net: where each face name goes and where to cut,
// fold and glue.
type NetLayout struct {
	Placed    []PlacedName
	Tabs      [][]PointF
	Cut, Fold []Seg
	W, H      float64
}

type PlacedName struct {
	Name           string
	X0, Y0, X1, Y1 float64
}

// BuildNetLayout lays the six faces of a box out in a cross.
func BuildNetLayout(size [3]float64, order, tabSide string) *NetLayout {
	w, h, d := size[0], size[1], size[2]
	tab := 3.0
	if d >= 8 {
		tab = 4
	}
	widths := map[string]float64{"right": d, "front": w, "left": d, "back": w}
	x0 := 0.0
	if tabSide == "left" {
		x0 = tab
	}
	net := &NetLayout{}
	columns := map[string][2]float64{}
	rowTop, rowBot := d, d+h
	x := x0
	for _, name := range RowOrders[order] {
		fw := widths[name]
		columns[name] = [2]float64{x, x + fw}
		net.Placed = append(net.Placed, PlacedName{name, x, rowTop, x + fw, rowTop + h})
		x += fw
	}
	xEnd := x

	fx0, fx1 := columns["front"][0], columns["front"][1]
	net.Placed = append(net.Placed, PlacedName{"top", fx0, 0, fx1, d})
	net.Placed = append(net.Placed, PlacedName{"bottom", fx0, rowBot, fx1, rowBot + d})

	// Fold lines: every internal face border.
	net.Fold = append(net.Fold, Seg{{x0, rowTop}, {xEnd, rowTop}})
	net.Fold = append(net.Fold, Seg{{x0, rowBot}, {xEnd, rowBot}})
	for _, name := range RowOrders[order] {
		if a := columns[name][0]; a != x0 {
			net.Fold = append(net.Fold, Seg{{a, rowTop}, {a, rowBot}})
		}
	}

	// Cut lines: free edges of top/bottom faces.
	for _, ys := range [][2]float64{{0, rowTop}, {rowBot + d, rowBot}} {
		yOut, yIn := ys[0], ys[1]
		net.Cut = append(net.Cut, Seg{{fx0, yOut}, {fx1, yOut}})
		net.Cut = append(net.Cut, Seg{{fx0, yOut}, {fx0, yIn}})
		net.Cut = append(net.Cut, Seg{{fx1, yOut}, {fx1, yIn}})
	}

	// Side glue flap on one end, the other end is a cut.
	if tabSide == "left" {
		net.Tabs = append(net.Tabs, Flap(PointF{x0, rowBot}, PointF{x0, rowTop}, [2]float64{-1, 0}, tab))
		net.Fold = append(net.Fold, Seg{{x0, rowTop}, {x0, rowBot}})
		net.Cut = append(net.Cut, Seg{{xEnd, rowTop}, {xEnd, rowBot}})
	} else {
		net.Tabs = append(net.Tabs, Flap(PointF{xEnd, rowTop}, PointF{xEnd, rowBot}, [2]float64{1, 0}, tab))
		net.Fold = append(net.Fold, Seg{{xEnd, rowTop}, {xEnd, rowBot}})
		net.Cut = append(net.Cut, Seg{{x0, rowTop}, {x0, rowBot}})
	}

	// Top/bottom flaps on every row face except the front.
	for _, name := range RowOrders[order] {
		a, b := columns[name][0], columns[name][1]
		if name == "front" {
			continue
		}
		net.Tabs = append(net.Tabs, Flap(PointF{a, rowTop}, PointF{b, rowTop}, [2]float64{0, -1}, tab))
		net.Tabs = append(net.Tabs, Flap(PointF{a, rowBot}, PointF{b, rowBot}, [2]float64{0, 1}, tab))
	}

	net.W, net.H = xEnd, rowBot+d
	if tabSide == "right" {
		net.W = xEnd + tab
	}
	return net
}

// BuildNet lays the six faces of a box out in a cross.
func BuildNet(faces map[string]*Img, size [3]float64, order, tabSide string) *Net {
	layout := BuildNetLayout(size, order, tabSide)
	net := &Net{Tabs: layout.Tabs, Cut: layout.Cut, Fold: layout.Fold, W: layout.W, H: layout.H}
	for _, p := range layout.Placed {
		net.Faces = append(net.Faces, PlacedFace{faces[p.Name], p.X0, p.Y0, p.X1, p.Y1})
	}
	return net
}

// BuildShellNet builds the net of the second layer: a slightly bigger box with no glue
// flaps. Only its opaque pixels get printed and cut out.
func BuildShellNet(faces map[string]*Img, size [3]int, inflate float64, order string) *Net {
	w, h, d := float64(size[0])+2*inflate, float64(size[1])+2*inflate, float64(size[2])+2*inflate
	widths := map[string]float64{"right": d, "front": w, "left": d, "back": w}
	net := &Net{}
	columns := map[string][2]float64{}
	x := 0.0
	for _, name := range RowOrders[order] {
		columns[name] = [2]float64{x, x + widths[name]}
		net.Faces = append(net.Faces, PlacedFace{faces[name], x, d, x + widths[name], d + h})
		if x != 0 {
			net.Fold = append(net.Fold, Seg{{x, d}, {x, d + h}})
		}
		x += widths[name]
	}
	fx0, fx1 := columns["front"][0], columns["front"][1]
	net.Faces = append(net.Faces, PlacedFace{faces["top"], fx0, 0, fx1, d})
	net.Faces = append(net.Faces, PlacedFace{faces["bottom"], fx0, d + h, fx1, 2*d + h})
	net.Fold = append(net.Fold, Seg{{fx0, d}, {fx1, d}})
	net.Fold = append(net.Fold, Seg{{fx0, d + h}, {fx1, d + h}})
	net.W, net.H = x, 2*d+h
	return net
}

// DrawNet draws a base-layer net onto the page and marks its area in the taken mask.
func DrawNet(page *Img, net *Net, ox, oy int, px int, lineW int, taken *Gray) {
	pt := func(p PointF) PointF { return PointF{float64(ox) + p.X*float64(px), float64(oy) + p.Y*float64(px)} }
	white := color.NRGBA{255, 255, 255, 255}
	black := color.NRGBA{0, 0, 0, 255}

	for _, poly := range net.Tabs {
		pts := make([]PointF, len(poly))
		for i, p := range poly {
			pts[i] = pt(p)
		}
		Polygon(page, pts, white)
		gpts := pts
		PolygonGray(taken, gpts, 255)
		LineGray(taken, gpts, 255, lineW, true)
		Line(page, gpts, black, float64(lineW), true)
	}

	for _, f := range net.Faces {
		iw, ih := f.Img.Rect.Dx(), f.Img.Rect.Dy()
		big := ResizeNearest(f.Img, iw*px, ih*px)
		PasteMask(page, big, ox+int(f.X0)*px, oy+int(f.Y0)*px, GrayAlpha(big))
		FillRectGray(taken,
			ox+int(f.X0)*px-lineW, oy+int(f.Y0)*px-lineW,
			ox+int(f.X1)*px+lineW, oy+int(f.Y1)*px+lineW, 255)
	}

	for _, s := range net.Fold {
		DashedLine(page, pt(s[0]), pt(s[1]), float64(max(1, lineW/2)), float64(px/5), color.NRGBA{90, 90, 90, 255})
	}
	for _, s := range net.Cut {
		Line(page, []PointF{pt(s[0]), pt(s[1])}, black, float64(lineW), false)
	}
}

// PolygonGray fills a polygon in a mask.
func PolygonGray(im *Gray, pts []PointF, v uint8) {
	if len(pts) < 3 {
		return
	}
	minY, maxY := math.Inf(1), math.Inf(-1)
	for _, p := range pts {
		minY, maxY = math.Min(minY, p.Y), math.Max(maxY, p.Y)
	}
	y0 := clampInt(int(math.Floor(minY)), 0, im.Rect.Dy()-1)
	y1 := clampInt(int(math.Ceil(maxY)), 0, im.Rect.Dy()-1)
	var cross []float64
	for y := y0; y <= y1; y++ {
		cy := float64(y) + 0.5
		cross = cross[:0]
		for i := range pts {
			a, b := pts[i], pts[(i+1)%len(pts)]
			if (a.Y <= cy && b.Y > cy) || (b.Y <= cy && a.Y > cy) {
				cross = append(cross, a.X+(cy-a.Y)/(b.Y-a.Y)*(b.X-a.X))
			}
		}
		for i := 0; i+1 < len(cross); i += 2 {
			xa, xb := cross[i], cross[i+1]
			if xa > xb {
				xa, xb = xb, xa
			}
			X0 := clampInt(int(math.Ceil(xa-0.5)), 0, im.Rect.Dx()-1)
			X1 := clampInt(int(math.Ceil(xb-0.5))-1, 0, im.Rect.Dx()-1)
			for x := X0; x <= X1; x++ {
				im.Pix[y*im.Stride+x] = v
			}
		}
	}
}

// LineGray draws a thick segment in a mask.
func LineGray(im *Gray, pts []PointF, v uint8, width int, jointRound bool) {
	if len(pts) < 2 {
		return
	}
	half := float64(width) / 2
	draw := func(p0, p1 PointF) {
		dx, dy := p1.X-p0.X, p1.Y-p0.Y
		length := math.Hypot(dx, dy)
		if length == 0 {
			return
		}
		nx, ny := -dy/length*half, dx/length*half
		PolygonGray(im, []PointF{
			{p0.X + nx, p0.Y + ny}, {p1.X + nx, p1.Y + ny},
			{p1.X - nx, p1.Y - ny}, {p0.X - nx, p0.Y - ny},
		}, v)
	}
	for i := 0; i+1 < len(pts); i++ {
		draw(pts[i], pts[i+1])
	}
	if jointRound {
		for i := 1; i+1 < len(pts); i++ {
			PolygonGray(im, circle(pts[i], half), v)
		}
	}
}

// ShellPieces splits a second-layer net into separate cut-out pieces. Returns RGBA
// sprites: texture where opaque, black cut line around it, dashed folds where a piece
// wraps over a box edge, transparent everywhere else.
func ShellPieces(net *Net, px, lineW int) []*Img {
	pad := lineW * 2
	toPx := func(v float64) int { return int(math.Round(v*float64(px))) + pad }
	sw, sh := int(math.Round(net.W*float64(px)))+2*pad, int(math.Round(net.H*float64(px)))+2*pad

	// Rasterise every opaque texel with its own id to find which ones touch.
	labels := make([]int32, sw*sh)
	type texel struct {
		x0, y0, x1, y1 int
		r, g, b        uint8
	}
	var texels []texel
	for _, f := range net.Faces {
		w, h := f.Img.Rect.Dx(), f.Img.Rect.Dy()
		xs := make([]int, w+1)
		ys := make([]int, h+1)
		for i := 0; i < w; i++ {
			xs[i] = toPx(f.X0 + (f.X1-f.X0)*float64(i)/float64(w))
		}
		xs[w] = toPx(f.X1)
		for j := 0; j < h; j++ {
			ys[j] = toPx(f.Y0 + (f.Y1-f.Y0)*float64(j)/float64(h))
		}
		ys[h] = toPx(f.Y1)
		for j := 0; j < h; j++ {
			for i := 0; i < w; i++ {
				c := GetPixel(f.Img, i, j)
				if c.A < ALPHACutoff {
					continue
				}
				a := float64(c.A)
				blend := func(v uint8) uint8 {
					return uint8(math.Round(float64(v)*a/255 + 255*(1-a/255)))
				}
				texels = append(texels, texel{xs[i], ys[j], xs[i+1] - 1, ys[j+1] - 1, blend(c.R), blend(c.G), blend(c.B)})
				id := int32(len(texels))
				for y := texels[len(texels)-1].y0; y <= texels[len(texels)-1].y1 && y < sh; y++ {
					for x := texels[len(texels)-1].x0; x <= texels[len(texels)-1].x1 && x < sw; x++ {
						labels[y*sw+x] = id
					}
				}
			}
		}
	}

	parent := make([]int, len(texels)+1)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(k int) int {
		for parent[k] != k {
			parent[k] = parent[parent[k]]
			k = parent[k]
		}
		return k
	}

	for k, t := range texels {
		for _, p := range [][2]int{{t.x1 + 1, (t.y0 + t.y1) / 2}, {(t.x0 + t.x1) / 2, t.y1 + 1}} {
			x, y := p[0], p[1]
			if x < sw && y < sh {
				if n := labels[y*sw+x]; n != 0 {
					parent[find(int(n))] = find(k + 1)
				}
			}
		}
	}

	groups := map[int]int{}
	var order [][]texel
	for k, t := range texels {
		root := find(k + 1)
		gi, ok := groups[root]
		if !ok {
			gi = len(order)
			groups[root] = gi
			order = append(order, nil)
		}
		order[gi] = append(order[gi], t)
	}

	var sprites []*Img
	for _, group := range order {
		bx0, by0 := group[0].x0, group[0].y0
		bx1, by1 := group[0].x1, group[0].y1
		for _, t := range group {
			bx0, by0 = min(bx0, t.x0), min(by0, t.y0)
			bx1, by1 = max(bx1, t.x1), max(by1, t.y1)
		}
		bx0, by0, bx1, by1 = bx0-pad, by0-pad, bx1+pad+1, by1+pad+1
		w, h := bx1-bx0, by1-by0
		colorImg := NewImg(w, h, color.NRGBA{255, 255, 255, 255})
		mask := imageNewGray(w, h)
		for _, t := range group {
			FillRect(colorImg, t.x0-bx0, t.y0-by0, t.x1-bx0, t.y1-by0, color.NRGBA{t.r, t.g, t.b, 255})
			FillRectGray(mask, t.x0-bx0, t.y0-by0, t.x1-bx0, t.y1-by0, 255)
		}

		// Cut line: a ring just outside the opaque area (also outlines holes).
		ring := GraySubtract(MaxFilter(mask, lineW), mask)
		// Fold lines: only where both sides of a box edge belong to this piece.
		folds := imageNewGray(w, h)
		for _, s := range net.Fold {
			DashedLineGray(folds,
				PointF{float64(toPx(s[0].X) - bx0), float64(toPx(s[0].Y) - by0)},
				PointF{float64(toPx(s[1].X) - bx0), float64(toPx(s[1].Y) - by0)},
				float64(max(1, lineW/2)), float64(px/5), 255)
		}
		folds = GrayMultiply(folds, MinFilter(mask, 1))

		sprite := NewImg(w, h, color.NRGBA{})
		PasteMask(sprite, colorImg, 0, 0, mask)
		PasteMask(sprite, NewImg(w, h, color.NRGBA{90, 90, 90, 255}), 0, 0, folds)
		PasteMask(sprite, NewImg(w, h, color.NRGBA{0, 0, 0, 255}), 0, 0, ring)
		sprites = append(sprites, sprite)
	}
	return sprites
}

// DashedLineGray draws a dashed line in a mask.
func DashedLineGray(im *Gray, p0, p1 PointF, width, dash float64, v uint8) {
	length := math.Hypot(p1.X-p0.X, p1.Y-p0.Y)
	if length == 0 {
		return
	}
	steps := int(length/(dash*2)) + 1
	for i := 0; i < steps; i++ {
		a := float64(i) * 2 * dash / length
		b := math.Min(float64(i*2+1)*dash/length, 1)
		if a >= 1 {
			break
		}
		LineGray(im, []PointF{
			{p0.X + (p1.X-p0.X)*a, p0.Y + (p1.Y-p0.Y)*a},
			{p0.X + (p1.X-p0.X)*b, p0.Y + (p1.Y-p0.Y)*b},
		}, v, int(math.Round(width)), false)
	}
}

func imageNewGray(w, h int) *Gray {
	return NewGray(imageRect(w, h))
}
