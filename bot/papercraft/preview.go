package papercraft

import (
	"fmt"
	"image"
	"math"
	"sort"
)

// renderPreview is an orthographic render of the model seen from the front, a bit from
// the left and above. With a focus box, only the cubes touching it are drawn and the
// frame is fitted to it: a zoomed view of one part of a big model. owner[y*size+x] is
// the index of the element seen at that pixel, or -1.
func renderPreview(elements []*Element, textures []*Texture, size int, yaw, pitch float64, focus *[2]V3) (*Img, []int32) {
	view := matMul(rotMatrix(V3{pitch, 0, 0}), rotMatrix(V3{0, yaw, 0}))
	cam := func(p V3) V3 {
		q := applyM(view, p, V3{})
		return V3{-q[0], -q[1], q[2]}
	}

	type quad struct {
		pts   [3]V3
		img   *Img
		index int
	}
	var quads []quad
	for index, e := range elements {
		if focus != nil && !e.clipToFocus(*focus) {
			continue
		}
		dims := e.faceDims()
		corners := e.faceCorners()
		for _, name := range faceOrder {
			fw, fh := dims[name][0], dims[name][1]
			if fw < eps || fh < eps {
				continue
			}
			face := e.Faces[name]
			tw, th := texelSize(textures, face)
			img := faceImage(textures, face, tw, th)
			if img == nil {
				continue
			}
			c := corners[name]
			quads = append(quads, quad{
				[3]V3{cam(e.toWorld(c[0])), cam(e.toWorld(c[1])), cam(e.toWorld(c[2]))},
				img, index,
			})
		}
	}
	if len(quads) == 0 {
		return nil, nil
	}

	var xs, ys []float64
	if focus != nil {
		for _, x := range []float64{focus[0][0], focus[1][0]} {
			for _, y := range []float64{focus[0][1], focus[1][1]} {
				for _, z := range []float64{focus[0][2], focus[1][2]} {
					p := cam(V3{x, y, z})
					xs, ys = append(xs, p[0]), append(ys, p[1])
				}
			}
		}
	} else {
		for _, q := range quads {
			for _, p := range q.pts {
				xs, ys = append(xs, p[0]), append(ys, p[1])
			}
			xs = append(xs, q.pts[1][0]+q.pts[2][0]-q.pts[0][0])
			ys = append(ys, q.pts[1][1]+q.pts[2][1]-q.pts[0][1])
		}
	}
	extentX := maxOf(xs) - minOf(xs)
	extentY := maxOf(ys) - minOf(ys)
	scale := 0.92 * float64(size) / math.Max(extentX, extentY)
	cx, cy := (maxOf(xs)+minOf(xs))/2, (maxOf(ys)+minOf(ys))/2
	screen := func(p V3) (float64, float64) {
		return float64(size)/2 + (p[0]-cx)*scale, float64(size)/2 + (p[1]-cy)*scale
	}

	// Plain z-buffer: faces of thin decals sit a hair above other faces, painter's
	// sorting gets those wrong.
	colorBuf := make([]byte, 3*size*size)
	for i := range colorBuf {
		colorBuf[i] = 0xff
	}
	depth := make([]float64, size*size)
	for i := range depth {
		depth[i] = math.Inf(1)
	}
	owner := make([]int32, size*size)
	for i := range owner {
		owner[i] = -1
	}

	for _, q := range quads {
		ax, ay := screen(q.pts[0])
		bx, by := screen(q.pts[1])
		lx, ly := screen(q.pts[2])
		az, bz, lz := q.pts[0][2], q.pts[1][2], q.pts[2][2]
		ex, ey, fx, fy := bx-ax, by-ay, lx-ax, ly-ay
		det := ex*fy - ey*fx
		if math.Abs(det) < 1e-6 {
			continue
		}
		// Light from the viewer: faces turned away get darker.
		p, r1, r2 := q.pts[0], q.pts[1], q.pts[2]
		n := V3{
			(r1[1]-p[1])*(r2[2]-p[2]) - (r1[2]-p[2])*(r2[1]-p[1]),
			(r1[2]-p[2])*(r2[0]-p[0]) - (r1[0]-p[0])*(r2[2]-p[2]),
			(r1[0]-p[0])*(r2[1]-p[1]) - (r1[1]-p[1])*(r2[0]-p[0]),
		}
		shade := 0.7 + 0.3*math.Abs(n[2])/(math.Hypot(n[0], math.Hypot(n[1], n[2]))+1e-300)
		tex, tw, th := q.img, q.img.Rect.Dx(), q.img.Rect.Dy()
		x0 := max(0, int(math.Min(math.Min(ax, bx), math.Min(lx, bx+fx))))
		x1 := min(size, int(math.Max(math.Max(ax, bx), math.Max(lx, bx+fx)))+1)
		y0 := max(0, int(math.Min(math.Min(ay, by), math.Min(ly, by+fy))))
		y1 := min(size, int(math.Max(math.Max(ay, by), math.Max(ly, by+fy)))+1)
		for Y := y0; Y < y1; Y++ {
			ry := float64(Y) + 0.5 - ay
			for X := x0; X < x1; X++ {
				rx := float64(X) + 0.5 - ax
				s := (rx*fy - ry*fx) / det
				t := (ex*ry - ey*rx) / det
				if !(s >= 0 && s < 1 && t >= 0 && t < 1) {
					continue
				}
				z := az + s*(bz-az) + t*(lz-az)
				k := Y*size + X
				if z >= depth[k] {
					continue
				}
				tx, ty := int(s*float64(tw)), int(t*float64(th))
				ti := ty*tex.Stride + tx*4
				if tex.Pix[ti+3] < 128 {
					continue
				}
				depth[k] = z
				owner[k] = int32(q.index)
				colorBuf[3*k] = uint8(float64(tex.Pix[ti]) * shade)
				colorBuf[3*k+1] = uint8(float64(tex.Pix[ti+1]) * shade)
				colorBuf[3*k+2] = uint8(float64(tex.Pix[ti+2]) * shade)
			}
		}
	}
	out := image.NewNRGBA(image.Rect(0, 0, size, size))
	for i := 0; i < size*size; i++ {
		out.Pix[i*4] = colorBuf[3*i]
		out.Pix[i*4+1] = colorBuf[3*i+1]
		out.Pix[i*4+2] = colorBuf[3*i+2]
		out.Pix[i*4+3] = 255
	}
	return out, owner
}

func maxOf(v []float64) float64 {
	m := math.Inf(-1)
	for _, x := range v {
		m = math.Max(m, x)
	}
	return m
}

func minOf(v []float64) float64 {
	m := math.Inf(1)
	for _, x := range v {
		m = math.Min(m, x)
	}
	return m
}

type view struct {
	owner []int32
	size  int
	x0    int
	y0    int
}

type markStat struct {
	count  int
	sx, sy float64
	pixels []int
}

// drawMarks numbers every piece of `indices` on the view where most of it is seen;
// pieces hidden in all views are skipped.
func drawMarks(page *Img, views []view, indices []int, elements []*Element, numbers map[*Element]int, small *Font, r, lineW int) {
	wanted := map[int]bool{}
	for _, i := range indices {
		wanted[i] = true
	}
	seen := make([][]markStat, len(views))
	for v := range views {
		seen[v] = make([]markStat, len(elements))
	}
	for v, vw := range views {
		for k, index := range vw.owner {
			if index < 0 || !wanted[int(index)] {
				continue
			}
			st := &seen[v][index]
			if st.count%7 == 0 {
				st.pixels = append(st.pixels, k)
			}
			st.count++
			st.sx += float64(k % vw.size)
			st.sy += float64(k / vw.size)
		}
	}
	type mark struct{ x, y int }
	var marks []mark
	for _, i := range indices {
		best := 0
		for v := range views {
			if seen[v][i].count > seen[best][i].count {
				best = v
			}
		}
		st := seen[best][i]
		if st.count == 0 {
			continue
		}
		vw := views[best]
		mx, my := st.sx/float64(st.count), st.sy/float64(st.count)
		clash := func(k int) float64 {
			total := 0.0
			X, Y := vw.x0+k%vw.size, vw.y0+k/vw.size
			for _, m := range marks {
				total += math.Max(0, 2*float64(r)-math.Hypot(float64(X-m.x), float64(Y-m.y)))
			}
			return total
		}
		bestK, bestScore := st.pixels[0], math.Inf(1)
		for _, k := range st.pixels {
			c := clash(k) * 50
			score := c*c + math.Pow(float64(k%vw.size)-mx, 2) + math.Pow(float64(k/vw.size)-my, 2)
			if score < bestScore {
				bestK, bestScore = k, score
			}
		}
		X, Y := vw.x0+bestK%vw.size, vw.y0+bestK/vw.size
		marks = append(marks, mark{X, Y})
		Ellipse(page,
			float64(X-r), float64(Y-r), float64(X+r), float64(Y+r),
			white, black, float64(lineW))
		small.Draw(page, float64(X), float64(Y), fmt.Sprint(numbers[elements[i]]), "mm", black)
	}
}

// legendPage is the assembly reference: the assembled model (clean when it has too many
// pieces for numbers) and zoomed part views with every piece numbered.
func legendPage(elements []*Element, numbers map[*Element]int, textures []*Texture, dpi int, credit string) []*Img {
	pageW, pageH, margin := PageGeometry(dpi)
	mm := float64(dpi) / 25.4
	font := NewFont(math.Round(3 * mm))
	small := NewFont(math.Round(2.4 * mm))
	r := int(math.Round(2.2 * mm))
	lineW := max(2, int(math.Round(0.2*mm)))
	size := (pageW - 2*margin - int(math.Round(6*mm))) / 2
	gap := int(math.Round(6 * mm))
	top := margin + int(math.Round(7*mm))

	newPage := func(title string) *Img {
		page := NewImg(pageW, pageH, white)
		if credit != "" {
			font.Draw(page, float64(margin), float64(margin/2), credit, "lm", black)
		}
		if title != "" {
			NewFont(math.Round(5*mm)).Draw(page, float64(margin), float64(margin+int(math.Round(2*mm))), title, "lm", black)
		}
		return page
	}

	drawNotes := func(page *Img, y int, parts bool) int {
		notes := "Сплошные линии — резать, пунктир — сгибать, белые клапаны — клеить. " +
			"Плоские детали сгибаются пополам и склеиваются изнанкой; вырезные (волоски и т.п.) " +
			"печатаются лицом и изнанкой: склейте их спинками, клапаны разведите в стороны " +
			"и приклейте к фигурке. Номер на детали тот же, что на картинке, — по нему видно, " +
			"куда она встаёт. Заштрихованная грань — скошенный срез: приклейте её к соседней " +
			"детали, и деталь сама встанет под нужным углом. Деталь, разрезанная соседями, печатается частями (12, 12-2, 12-3): " +
			"склейте их заштрихованными гранями. Внешние полупрозрачные слои " +
			"вырезаются по граням (подписаны перед, зад, лево, право, верх, низ) и наклеиваются " +
			"поверх детали на том же месте."
		if parts {
			notes += " Модель большая: на общем виде все номера не помещаются, ищите каждую " +
				"деталь по частям на следующих страницах."
		}
		line := ""
		for _, w := range splitWords(notes) {
			if small.Width(line+" "+w) > float64(pageW-2*margin) && line != "" {
				small.Draw(page, float64(margin), float64(y), line, "lt", black)
				y, line = y+int(math.Round(3.6*mm)), w
			} else if line == "" {
				line = w
			} else {
				line += " " + w
			}
		}
		small.Draw(page, float64(margin), float64(y), line, "lt", black)
		return y
	}

	pasted := func(page *Img, y int, focus *[2]V3) []view {
		var views []view
		for col, yaw := range []float64{30, 210} {
			img, owner := renderPreview(elements, textures, size, yaw, -20, focus)
			x0 := margin + col*(size+gap)
			Paste(page, img, x0, y)
			views = append(views, view{owner, size, x0, y})
		}
		return views
	}

	all := make([]int, len(elements))
	for i := range all {
		all[i] = i
	}
	if len(elements) <= maxMarks {
		page := newPage("Сборка")
		drawMarks(page, pasted(page, top, nil), all, elements, numbers, small, r, lineW)
		drawNotes(page, top+size+gap, false)
		return []*Img{page}
	}

	// Too many pieces for one view: split the model along its longest axis into bands of
	// a readable size, print the overview clean and number the pieces part by part.
	hiV := V3{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	loV := V3{math.Inf(1), math.Inf(1), math.Inf(1)}
	for _, e := range elements {
		lo, hi := e.WorldBox()
		for i := range 3 {
			loV[i], hiV[i] = math.Min(loV[i], lo[i]), math.Max(hiV[i], hi[i])
		}
	}
	axis := 0
	for i := 1; i < 3; i++ {
		if hiV[i]-loV[i] > hiV[axis]-loV[axis] {
			axis = i
		}
	}
	order := make([]int, len(elements))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		la, ha := elements[order[a]].WorldBox()
		lb, hb := elements[order[b]].WorldBox()
		return (la[axis]+ha[axis])/2 < (lb[axis]+hb[axis])/2
	})
	var bands [][]int
	for i := 0; i < len(order); i += maxMarks {
		bands = append(bands, order[i:min(i+maxMarks, len(order))])
	}

	page := newPage("Сборка — общий вид")
	pasted(page, top, nil)
	drawNotes(page, top+size+gap, true)
	pages := []*Img{page}

	perPage := max(1, (pageH-top-margin)/(size+gap))
	for p := 0; p < len(bands); p += perPage {
		first := p + 1
		last := min(p+perPage, len(bands))
		part := fmt.Sprintf("часть %d", first)
		if first != last {
			part = fmt.Sprintf("части %d–%d", first, last)
		}
		page := newPage(fmt.Sprintf("Сборка — %s из %d", part, len(bands)))
		for row, band := range bands[p:last] {
			blo := V3{math.Inf(1), math.Inf(1), math.Inf(1)}
			bhi := V3{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
			for _, i := range band {
				lo, hi := elements[i].WorldBox()
				for k := range 3 {
					blo[k], bhi[k] = math.Min(blo[k], lo[k]), math.Max(bhi[k], hi[k])
				}
			}
			var focus [2]V3
			for k := range 3 {
				pad := 0.08 * (bhi[k] - blo[k]) // a bit of the neighbouring bands stays in frame
				focus[0][k], focus[1][k] = blo[k]-pad, bhi[k]+pad
			}
			f := focus
			views := pasted(page, top+row*(size+gap), &f)
			drawMarks(page, views, band, elements, numbers, small, r, lineW)
		}
		pages = append(pages, page)
	}
	return pages
}

func splitWords(s string) []string {
	var out []string
	start := -1
	for i, r := range s {
		if r == ' ' || r == '\n' || r == '\t' {
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, s[start:])
	}
	return out
}
