package papercraft

import (
	"image/color"
	"math"
	"sort"
)

// SolidFace is one face of a convex solid: points, outward normal, and its source face
// with texture corners when it is a textured face (caps have no source).
type SolidFace struct {
	Pts    []V3
	N      V3
	Src    string
	Tex    [3]V3
	HasSrc bool
}

// Solid is a convex solid as a list of faces.
type Solid []*SolidFace

var faceNormals = map[string]V3{
	"north": {0, 0, -1}, "south": {0, 0, 1}, "east": {1, 0, 0},
	"west": {-1, 0, 0}, "up": {0, 1, 0}, "down": {0, -1, 0},
}

// BoxSolid is the cube as a convex solid.
func boxSolid(e *Element) Solid {
	var solid Solid
	corners := e.faceCorners()
	for _, name := range faceOrder {
		tl, tr, bl := corners[name][0], corners[name][1], corners[name][2]
		br := vAdd(tr, vSub(bl, tl))
		solid = append(solid, &SolidFace{Pts: []V3{tl, tr, br, bl}, N: faceNormals[name], Src: name, Tex: [3]V3{tl, tr, bl}, HasSrc: true})
	}
	return solid
}

// PolyArea is the area of a planar polygon with normal n.
func PolyArea(pts []V3, n V3) float64 {
	total := V3{}
	for i := range pts {
		total = vAdd(total, vCross(pts[i], pts[(i+1)%len(pts)]))
	}
	return math.Abs(vDot(total, n)) / 2
}

func solidVolume(s Solid) float64 {
	total := 0.0
	for _, f := range s {
		total += PolyArea(f.Pts, f.N) * vDot(f.N, f.Pts[0])
	}
	return total / 3
}

// Clip keeps the part of a convex solid behind the plane (p0, outward normal n).
func Clip(solid Solid, p0, n V3, eps float64) Solid {
	var out Solid
	var cap []V3
	flush := false
	for _, f := range solid {
		pts := f.Pts
		var kept []V3
		allOn := true
		for _, q := range pts {
			if math.Abs(vDot(n, vSub(q, p0))) > eps {
				allOn = false
				break
			}
		}
		if allOn {
			// The face lies on the plane: it is the cap itself, or the solid is flat there.
			if vDot(f.N, n) > 0 {
				out = append(out, f)
				flush = true
			}
			continue
		}
		for i := range pts {
			a, b := pts[i], pts[(i+1)%len(pts)]
			da, db := vDot(n, vSub(a, p0)), vDot(n, vSub(b, p0))
			if da <= eps {
				kept = append(kept, a)
				if da >= -eps {
					cap = append(cap, a)
				}
			}
			if (da < -eps && db > eps) || (db < -eps && da > eps) {
				x := vAdd(a, vMul(vSub(b, a), da/(da-db)))
				kept = append(kept, x)
				cap = append(cap, x)
			}
		}
		if len(kept) > 1 {
			dedup := kept[:1]
			for i := 1; i < len(kept); i++ {
				if key3(kept[i]) != key3(dedup[len(dedup)-1]) {
					dedup = append(dedup, kept[i])
				}
			}
			kept = dedup
		}
		if len(kept) >= 3 && PolyArea(kept, f.N) > eps {
			out = append(out, &SolidFace{Pts: kept, N: f.N, Src: f.Src, Tex: f.Tex, HasSrc: f.HasSrc})
		}
	}
	var uniq []V3
	seen := map[V3]bool{}
	for _, p := range cap {
		if k := key3(p); !seen[k] {
			seen[k] = true
			uniq = append(uniq, p)
		}
	}
	cap = uniq
	if len(cap) >= 3 && !flush {
		c := V3{}
		for _, p := range cap {
			c = vAdd(c, p)
		}
		c = vMul(c, 1/float64(len(cap)))
		u := vUnit(vSub(cap[0], c))
		w := vCross(n, u)
		sort.SliceStable(cap, func(i, j int) bool {
			pi, pj := vSub(cap[i], c), vSub(cap[j], c)
			return math.Atan2(vDot(pi, w), vDot(pi, u)) < math.Atan2(vDot(pj, w), vDot(pj, u))
		})
		if PolyArea(cap, n) > eps {
			out = append(out, &SolidFace{Pts: cap, N: n})
		}
	}
	// A single face is a polygon being clipped (a plane piece); a solid needs four faces.
	if len(solid) == 1 || len(out) >= 4 {
		return out
	}
	return nil
}

// Plane is a point and an outward unit normal.
type Plane struct {
	P0, N V3
}

// PlanesIn returns the six face planes of `other` in e's coordinates.
func planesIn(e, other *Element) []Plane {
	lo, hi := other.box()
	var planes []Plane
	for i := range 3 {
		for _, side := range []struct {
			v V3
			n float64
		}{{lo, -1}, {hi, 1}} {
			p := lo
			p[i] = side.v[i]
			q := p
			q[i] += side.n
			a, b := e.toLocal(other.toWorld(p)), e.toLocal(other.toWorld(q))
			planes = append(planes, Plane{a, vUnit(vSub(b, a))})
		}
	}
	return planes
}

// SolidPlanes returns face planes of other's current solid in e's coordinates.
func solidPlanes(e, other *Element) []Plane {
	var planes []Plane
	for _, f := range other.Solid {
		p0 := e.toLocal(other.toWorld(f.Pts[0]))
		q := e.toLocal(other.toWorld(vAdd(f.Pts[0], f.N)))
		planes = append(planes, Plane{p0, vUnit(vSub(q, p0))})
	}
	return planes
}

func overlapVolume(a, b *Element) float64 {
	inside := a.Solid
	for _, pl := range solidPlanes(a, b) {
		inside = Clip(inside, pl.P0, pl.N, 1e-5)
		if len(inside) == 0 {
			return 0
		}
	}
	return solidVolume(inside)
}

func flatAxes(e *Element, thin float64) []int {
	var out []int
	size := e.size()
	for i, n := range size {
		if n < thin {
			out = append(out, i)
		}
	}
	return out
}

func boxesTouch(a, b *Element) bool {
	alo, ahi := a.WorldBox()
	blo, bhi := b.WorldBox()
	for i := range 3 {
		if !(alo[i] < bhi[i] && blo[i] < ahi[i]) {
			return false
		}
	}
	return true
}

func equalPoly(a []V3, b []V3) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ResolveOverlaps cuts cubes apart wherever they sink into each other, so paper pieces
// never collide. Returns buried pieces.
func resolveOverlaps(elements []*Element, thin float64) []*Element {
	var solids []*Element
	for _, e := range elements {
		if len(flatAxes(e, thin)) == 0 {
			solids = append(solids, e)
		}
	}
	for _, e := range elements {
		e.Cut, e.PlaneCut, e.Glued = nil, nil, nil
	}
	for _, e := range solids {
		e.Solid = boxSolid(e)
		e.Glued = nil
		e.Full = solidVolume(e.Solid)
	}
	type pair struct {
		v    float64
		a, b *Element
	}
	var pairs []pair
	for i, a := range solids {
		for _, b := range solids[i+1:] {
			if boxesTouch(a, b) {
				v := overlapVolume(a, b)
				if v > 1e-4*math.Min(a.Full, b.Full) {
					pairs = append(pairs, pair{v, a, b})
				}
			}
		}
	}
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].v > pairs[j].v })

	buried := map[*Element]bool{}
	for _, p := range pairs {
		a, b := p.a, p.b
		if buried[a] || buried[b] {
			continue
		}
		if overlapVolume(a, b) <= 1e-4*math.Min(a.Full, b.Full) {
			continue // an earlier cut already took the shared part away
		}
		var bestRemoved float64
		var bestCut, bestBy *Element
		var bestKept Solid
		first := true
		for _, cp := range [][2]*Element{{a, b}, {b, a}} {
			cut, by := cp[0], cp[1]
			before := solidVolume(cut.Solid)
			for _, pl := range solidPlanes(cut, by) {
				kept := Clip(cut.Solid, pl.P0, vMul(pl.N, -1), 1e-5)
				removed := before
				if len(kept) > 0 {
					removed -= solidVolume(kept)
				}
				if first || removed < bestRemoved {
					first = false
					bestRemoved, bestCut, bestBy, bestKept = removed, cut, by, kept
				}
			}
		}
		_ = bestBy
		if len(bestKept) == 0 || solidVolume(bestKept) < 0.02*bestCut.Full {
			buried[bestCut] = true
			continue
		}
		bestCut.Solid = bestKept
		bestCut.Glued = append(bestCut.Glued, bestBy)
	}
	for _, e := range solids {
		if len(e.Glued) > 0 {
			e.Cut = &e.Solid
		}
	}

	for _, e := range elements {
		axes := flatAxes(e, thin)
		if len(axes) != 1 {
			continue
		}
		first := planeFaceNames[axes[0]].first
		corners := e.faceCorners()
		tl, tr, bl := corners[first][0], corners[first][1], corners[first][2]
		br := vAdd(tr, vSub(bl, tl))
		face := Solid{&SolidFace{Pts: []V3{tl, tr, br, bl}, N: faceNormals[first], Src: first, HasSrc: true}}
		full := PolyArea(face[0].Pts, face[0].N)
		var glue [2]V3
		hasGlue := false
		for _, s := range solids {
			if buried[s] || !boxesTouch(e, s) {
				continue
			}
			pls := solidPlanes(e, s)
			inside := face
			for _, pl := range pls {
				inside = Clip(inside, pl.P0, pl.N, 1e-5)
			}
			if len(inside) == 0 || PolyArea(inside[0].Pts, face[0].N) < 1e-4*full {
				continue
			}
			var kept Solid
			var p0, n V3
			bestArea := -1.0
			for _, pl := range pls {
				c := Clip(face, pl.P0, vMul(pl.N, -1), 1e-5)
				area := 0.0
				if len(c) > 0 {
					area = PolyArea(c[0].Pts, face[0].N)
				}
				if area > bestArea {
					bestArea, p0, n, kept = area, pl.P0, pl.N, c
				}
			}
			if len(kept) == 0 || PolyArea(kept[0].Pts, face[0].N) < 0.05*full {
				buried[e] = true
				break
			}
			face = kept[:1]
			var edge []V3
			for _, q := range face[0].Pts {
				if math.Abs(vDot(n, vSub(q, p0))) < 1e-6 {
					edge = append(edge, q)
				}
			}
			if len(edge) >= 2 {
				glue, hasGlue = [2]V3{edge[0], edge[len(edge)-1]}, true
			}
		}
		if !buried[e] && !equalPoly(face[0].Pts, []V3{tl, tr, br, bl}) {
			e.PlaneCut = &PlaneCut{Poly: face[0].Pts, Glue: glue, HasGlue: hasGlue}
		}
	}
	var out []*Element
	for _, e := range elements {
		if buried[e] {
			out = append(out, e)
		}
	}
	return out
}

type edgeKey struct{ a, b V3 }

func makeEdgeKey(p, q V3) edgeKey {
	a, b := key3(p), key3(q)
	if lessV3(b, a) {
		a, b = b, a
	}
	return edgeKey{a, b}
}

func lessV3(a, b V3) bool {
	for i := range 3 {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

type foldEdge struct {
	length float64
	j      int
	k      edgeKey
}

// Unfold lays the faces of a convex solid flat without overlaps. Returns placed 2D points
// per face (same order as its 3D points) and the folds that stay connected.
func Unfold(solid Solid) ([][]PointF, []struct {
	I, J int
	K    edgeKey
}) {
	edges := map[edgeKey][]int{}
	for i, f := range solid {
		for s := range f.Pts {
			k := makeEdgeKey(f.Pts[s], f.Pts[(s+1)%len(f.Pts)])
			edges[k] = append(edges[k], i)
		}
	}
	neighbours := make([][]foldEdge, len(solid))
	for k, fs := range edges {
		if len(fs) != 2 {
			continue
		}
		length := 0.0
		if k.a != k.b {
			length = vDist(k.a, k.b)
		}
		neighbours[fs[0]] = append(neighbours[fs[0]], foldEdge{length, fs[1], k})
		neighbours[fs[1]] = append(neighbours[fs[1]], foldEdge{length, fs[0], k})
	}

	flat := func(f *SolidFace) []PointF {
		u := vUnit(vSub(f.Pts[1], f.Pts[0]))
		if f.HasSrc {
			tl, tr := f.Tex[0], f.Tex[1]
			u = vUnit(vSub(tr, tl))
		}
		down := vCross(u, f.N)
		out := make([]PointF, len(f.Pts))
		for i, p := range f.Pts {
			d := vSub(p, f.Pts[0])
			out[i] = PointF{vDot(d, u), vDot(d, down)}
		}
		return out
	}
	local := make([][]PointF, len(solid))
	for i, f := range solid {
		local[i] = flat(f)
	}

	roots := make([]int, len(solid))
	for i := range roots {
		roots[i] = i
	}
	sort.SliceStable(roots, func(i, j int) bool {
		a, b := solid[roots[i]], solid[roots[j]]
		if a.HasSrc != b.HasSrc {
			return a.HasSrc
		}
		return PolyArea(a.Pts, a.N) > PolyArea(b.Pts, b.N)
	})

	var bestArea float64
	var bestPolys [][]PointF
	var bestFolds []struct {
		I, J int
		K    edgeKey
	}
	for _, root := range roots {
		placed := map[int][]PointF{root: local[root]}
		type foldRec struct {
			I, J int
			K    edgeKey
		}
		var folds []foldRec
		queue := []int{root}
		for len(queue) > 0 {
			i := queue[0]
			queue = queue[1:]
			ns := append([]foldEdge(nil), neighbours[i]...)
			sort.SliceStable(ns, func(a, b int) bool { return ns[a].length > ns[b].length })
			for _, nb := range ns {
				j, k := nb.j, nb.k
				if _, ok := placed[j]; ok {
					continue
				}
				ia := pointIndex(solid[i].Pts, k.a)
				ib := pointIndex(solid[i].Pts, k.b)
				ja := pointIndex(solid[j].Pts, k.a)
				jb := pointIndex(solid[j].Pts, k.b)
				a2, b2 := placed[i][ia], placed[i][ib]
				a1, b1 := local[j][ja], local[j][jb]
				ang := math.Atan2(b2.Y-a2.Y, b2.X-a2.X) - math.Atan2(b1.Y-a1.Y, b1.X-a1.X)
				c, s := math.Cos(ang), math.Sin(ang)
				moved := make([]PointF, len(local[j]))
				for n, p := range local[j] {
					moved[n] = PointF{
						a2.X + c*(p.X-a1.X) - s*(p.Y-a1.Y),
						a2.Y + s*(p.X-a1.X) + c*(p.Y-a1.Y),
					}
				}
				placed[j] = moved
				folds = append(folds, foldRec{i, j, k})
				queue = append(queue, j)
			}
		}
		if len(placed) < len(solid) {
			continue
		}
		polys := make([][]PointF, len(solid))
		for i := range solid {
			polys[i] = placed[i]
		}
		bad := false
		for a := range polys {
			for b := 0; b < a; b++ {
				if overlap(polys[a], polys[b]) {
					bad = true
				}
			}
		}
		if bad {
			continue
		}
		minX, minY := math.Inf(1), math.Inf(1)
		maxX, maxY := math.Inf(-1), math.Inf(-1)
		for _, poly := range polys {
			for _, p := range poly {
				minX, minY = math.Min(minX, p.X), math.Min(minY, p.Y)
				maxX, maxY = math.Max(maxX, p.X), math.Max(maxY, p.Y)
			}
		}
		area := (maxX - minX) * (maxY - minY)
		if bestPolys == nil || area < bestArea {
			bestArea = area
			bestPolys = polys
			bestFolds = nil
			for _, f := range folds {
				bestFolds = append(bestFolds, struct {
					I, J int
					K    edgeKey
				}{f.I, f.J, f.K})
			}
		}
	}
	if bestPolys == nil {
		return nil, nil
	}
	return bestPolys, bestFolds
}

func pointIndex(pts []V3, target V3) int {
	for i, p := range pts {
		if key3(p) == target {
			return i
		}
	}
	return -1
}

// Overlap reports whether two convex polygons overlap by more than a touch.
func overlap(p, q []PointF) bool {
	for _, poly := range [][]PointF{p, q} {
		for i := range poly {
			a, b := poly[i], poly[(i+1)%len(poly)]
			nx, ny := a.Y-b.Y, b.X-a.X
			n := math.Hypot(nx, ny)
			if n < 1e-12 {
				continue
			}
			maxA, minA := math.Inf(-1), math.Inf(1)
			for _, pt := range p {
				v := (pt.X*nx + pt.Y*ny) / n
				maxA, minA = math.Max(maxA, v), math.Min(minA, v)
			}
			maxB, minB := math.Inf(-1), math.Inf(1)
			for _, pt := range q {
				v := (pt.X*nx + pt.Y*ny) / n
				maxB, minB = math.Max(maxB, v), math.Min(minB, v)
			}
			if maxA <= minB+1e-4 || maxB <= minA+1e-4 {
				return false
			}
		}
	}
	return true
}

// SlantedPiece lays a cut tilted cube flat, with glue flaps on the cut edges.
func slantedPiece(e *Element, solid Solid, textures []*Texture, unit, pxMM float64, label string) []*Img {
	polys, folds := Unfold(solid)
	if polys == nil {
		return nil
	}
	for i := range polys {
		for j := range polys[i] {
			polys[i][j].X, polys[i][j].Y = polys[i][j].X*unit, polys[i][j].Y*unit
		}
	}
	foldEdges := map[edgeKey]bool{}
	for _, f := range folds {
		foldEdges[f.K] = true
	}

	var flaps [][]PointF
	var cuts, flapFolds [][2]PointF
	seen := map[edgeKey]bool{}
	for i, f := range solid {
		for s := range f.Pts {
			k := makeEdgeKey(f.Pts[s], f.Pts[(s+1)%len(f.Pts)])
			if foldEdges[k] || k.a == k.b {
				continue
			}
			a2, b2 := polys[i][s], polys[i][(s+1)%len(f.Pts)]
			if seen[k] {
				cuts = append(cuts, [2]PointF{a2, b2})
				continue
			}
			poly := polys[i]
			cx, cy := 0.0, 0.0
			for _, p := range poly {
				cx, cy = cx+p.X, cy+p.Y
			}
			cx, cy = cx/float64(len(poly)), cy/float64(len(poly))
			length := math.Hypot(b2.X-a2.X, b2.Y-a2.Y)
			nx, ny := (b2.Y-a2.Y)/length, -(b2.X-a2.X)/length
			if nx*(a2.X-cx)+ny*(a2.Y-cy) < 0 {
				nx, ny = -nx, -ny
			}
			placed := false
			for _, depth := range []float64{3, 1.8} {
				depth = math.Min(depth, length*0.8)
				inset := math.Min(depth, length*0.35)
				dx, dy := (b2.X-a2.X)/length, (b2.Y-a2.Y)/length
				tab := []PointF{
					a2,
					{a2.X + dx*inset + nx*depth, a2.Y + dy*inset + ny*depth},
					{b2.X - dx*inset + nx*depth, b2.Y - dy*inset + ny*depth},
					b2,
				}
				hit := false
				for _, other := range polys {
					if overlap(tab, other) {
						hit = true
						break
					}
				}
				if !hit {
					for _, other := range flaps {
						if overlap(tab, other) {
							hit = true
							break
						}
					}
				}
				if !hit {
					flaps = append(flaps, tab)
					flapFolds = append(flapFolds, [2]PointF{a2, b2})
					seen[k] = true
					placed = true
					break
				}
			}
			if !placed {
				cuts = append(cuts, [2]PointF{a2, b2})
			}
		}
	}

	points := []PointF{}
	for _, poly := range polys {
		points = append(points, poly...)
	}
	for _, tab := range flaps {
		points = append(points, tab...)
	}
	blo, bhi := bounds(points)
	sheet := newSheet(blo, bhi, pxMM, label)
	for _, tab := range flaps {
		sheet.tab(tab)
	}
	for i, f := range solid {
		paintFace(sheet, e, f, polys[i], textures, unit)
	}
	for _, f := range folds {
		ia := pointIndex(solid[f.I].Pts, f.K.a)
		ib := pointIndex(solid[f.I].Pts, f.K.b)
		sheet.fold(polys[f.I][ia], polys[f.I][ib])
	}
	for _, ff := range flapFolds {
		sheet.fold(ff[0], ff[1])
	}
	for _, c := range cuts {
		sheet.cut(c[0], c[1])
	}
	return []*Img{sheet.img}
}

func paintFace(sheet *Sheet, e *Element, f *SolidFace, poly []PointF, textures []*Texture, unit float64) {
	pix := make([]PointF, len(poly))
	for i, p := range poly {
		pix[i] = sheet.pt(p)
	}
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, p := range pix {
		minX, minY = math.Min(minX, p.X), math.Min(minY, p.Y)
		maxX, maxY = math.Max(maxX, p.X), math.Max(maxY, p.Y)
	}
	X0, Y0 := int(minX), int(minY)
	X1, Y1 := int(maxX)+2, int(maxY)+2
	mask := NewGray(imageRect(X1-X0, Y1-Y0))
	pts := make([]PointF, len(pix))
	for i, p := range pix {
		pts[i] = PointF{p.X - float64(X0), p.Y - float64(Y0)}
	}
	PolygonGray(mask, pts, 255)
	tile := NewImg(mask.Rect.Dx(), mask.Rect.Dy(), white)
	img := (*Img)(nil)
	if f.HasSrc {
		fw, fh := e.faceDims()[f.Src][0], e.faceDims()[f.Src][1]
		W := max(1, int(math.Round(fw*unit*sheet.k)))
		H := max(1, int(math.Round(fh*unit*sheet.k)))
		img = faceImage(textures, e.Faces[f.Src], W, H)
	}
	if img != nil {
		tl, tr, bl := f.Tex[0], f.Tex[1], f.Tex[2]
		ux, vy := vSub(tr, tl), vSub(bl, tl)
		W, H := img.Rect.Dx(), img.Rect.Dy()
		st := make([]PointF, len(f.Pts))
		for i, p := range f.Pts {
			d := vSub(p, tl)
			st[i] = PointF{vDot(d, ux) / vDot(ux, ux) * float64(W), vDot(d, vy) / vDot(vy, vy) * float64(H)}
		}
		coeffs := affine(pts, st)
		if coeffs != nil {
			AlphaComposite(tile, transformAffine(img, mask.Rect.Dx(), mask.Rect.Dy(), *coeffs))
		}
	} else if !f.HasSrc {
		// The slanted glue face: hatched, it goes flat onto the neighbouring piece.
		step := max(4, int(math.Round(1.5*sheet.k)))
		hatch := color.NRGBA{200, 200, 200, 255}
		for x := -tile.Rect.Dy(); x < tile.Rect.Dx(); x += step {
			Line(tile, []PointF{{float64(x), float64(tile.Rect.Dy())}, {float64(x + tile.Rect.Dy()), 0}}, hatch, 1, false)
		}
	}
	PasteMask(sheet.img, tile, X0, Y0, mask)
}

// affine returns the coefficients of the affine map taking src points to dst points
// (first 3 non-collinear).
func affine(src, dst []PointF) *[6]float64 {
	for i := range src {
		for j := i + 1; j < len(src); j++ {
			for k := j + 1; k < len(src); k++ {
				x0, y0 := src[i].X, src[i].Y
				x1, y1 := src[j].X, src[j].Y
				x2, y2 := src[k].X, src[k].Y
				det := (x1-x0)*(y2-y0) - (x2-x0)*(y1-y0)
				if math.Abs(det) < 1e-6 {
					continue
				}
				var out [6]float64
				for c := 0; c < 2; c++ {
					var d0, d1, d2 float64
					if c == 0 {
						d0, d1, d2 = dst[i].X, dst[j].X, dst[k].X
					} else {
						d0, d1, d2 = dst[i].Y, dst[j].Y, dst[k].Y
					}
					a := ((d1-d0)*(y2-y0) - (d2-d0)*(y1-y0)) / det
					b := ((x1-x0)*(d2-d0) - (x2-x0)*(d1-d0)) / det
					out[c*3] = a
					out[c*3+1] = b
					out[c*3+2] = d0 - a*x0 - b*y0
				}
				return &out
			}
		}
	}
	return nil
}

// transformAffine samples src at (a*x + b*y + c) for every output pixel.
func transformAffine(src *Img, w, h int, coeffs [6]float64) *Img {
	out := NewImg(w, h, color.NRGBA{})
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sx := int(math.Floor(coeffs[0]*float64(x) + coeffs[1]*float64(y) + coeffs[2]))
			sy := int(math.Floor(coeffs[3]*float64(x) + coeffs[4]*float64(y) + coeffs[5]))
			if sx < 0 || sx >= sw || sy < 0 || sy >= sh {
				continue
			}
			copy(out.Pix[y*out.Stride+x*4:], src.Pix[sy*src.Stride+sx*4:sy*src.Stride+sx*4+4])
		}
	}
	return out
}
