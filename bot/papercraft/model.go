package papercraft

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"math"
	"regexp"
	"strings"
)

// V3 is a 3D point or vector.
type V3 = [3]float64

const (
	// Cubes thinner than this on paper become flat pieces, mm.
	thinMM = 0.5
	// Room taken by glue flaps, labels and padding around a piece, mm.
	pieceExtraMM = 12
	// Outer layers and what they wrap are real cubes, not decals: at least this thick.
	layerMin = 0.5
	// Whole-view numbers stop being readable past this many pieces.
	maxMarks = 60
)

var watermark = regexp.MustCompile(`(?i)watermark|вотермарк|ватермарк`)

const eps = 1e-6

// Texture is an embedded image and its texels-per-unit scale.
type Texture struct {
	Img    *Img
	ScaleX float64
	ScaleY float64
}

// Face is one textured face of a cube.
type Face struct {
	Texture  *int      `json:"texture"`
	UV       []float64 `json:"uv"`
	Rotation float64   `json:"rotation"`
}

// Group is a Blockbench outliner group.
type Group struct {
	Name     string            `json:"name"`
	UUID     string            `json:"uuid"`
	Origin   V3                `json:"origin"`
	Rotation V3                `json:"rotation"`
	Children []json.RawMessage `json:"children"`
}

// Element is a cube of the model plus the pipeline state filled in by the generator.
type Element struct {
	Name       string           `json:"name"`
	Type       string           `json:"type"`
	UUID       string           `json:"uuid"`
	From       V3               `json:"from"`
	To         V3               `json:"to"`
	Inflate    float64          `json:"inflate"`
	Rotation   V3               `json:"rotation"`
	Origin     V3               `json:"origin"`
	Faces      map[string]*Face `json:"faces"`
	Visibility *bool            `json:"visibility"`
	Export     *bool            `json:"export"`

	Groups []*Group

	xf       *Transform
	wb       [2]V3
	hasWB    bool
	Solid    Solid
	Cut      *Solid // set when the cube was cut apart from its neighbours
	Glued    []*Element
	PlaneCut *PlaneCut // a flat cube trimmed along a neighbour's face
	Over     *Element  // see-through outer layer wraps this cube
	Full     float64
	Buried   bool
}

// Transform is a rotation and offset: world = M·p + T.
type Transform struct {
	M [3][3]float64
	T V3
}

// PlaneCut is the kept polygon of a trimmed flat cube and the edge it glues on.
type PlaneCut struct {
	Poly    []V3
	Glue    [2]V3
	HasGlue bool
}

type bbmodel struct {
	Resolution struct {
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	} `json:"resolution"`
	Elements []*Element        `json:"elements"`
	Groups   []*Group          `json:"groups"`
	Outliner []json.RawMessage `json:"outliner"`
	Textures []struct {
		Source   string  `json:"source"`
		UVWidth  float64 `json:"uv_width"`
		UVHeight float64 `json:"uv_height"`
	} `json:"textures"`
}

// LoadModel parses a .bbmodel file with embedded textures.
func LoadModel(data []byte) ([]*Element, []*Texture, error) {
	var m bbmodel
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, nil, err
	}
	var textures []*Texture
	for _, t := range m.Textures {
		if !strings.HasPrefix(t.Source, "data:") {
			return nil, nil, errors.New("texture is not embedded in the model")
		}
		raw, err := base64.StdEncoding.DecodeString(t.Source[strings.IndexByte(t.Source, ',')+1:])
		if err != nil {
			return nil, nil, err
		}
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			return nil, nil, err
		}
		im := ToNRGBA(img)
		uvW, uvH := t.UVWidth, t.UVHeight
		if uvW == 0 {
			uvW = m.Resolution.Width
		}
		if uvH == 0 {
			uvH = m.Resolution.Height
		}
		if uvW == 0 {
			uvW = float64(im.Rect.Dx())
		}
		if uvH == 0 {
			uvH = float64(im.Rect.Dy())
		}
		textures = append(textures, &Texture{Img: im, ScaleX: float64(im.Rect.Dx()) / uvW, ScaleY: float64(im.Rect.Dy()) / uvH})
	}

	groups := map[string]*Group{}
	for _, g := range m.Groups {
		groups[g.UUID] = g
	}
	parents := map[string][]*Group{}
	var walk func(nodes []json.RawMessage, chain []*Group) error
	walk = func(nodes []json.RawMessage, chain []*Group) error {
		for _, raw := range nodes {
			var name string
			if json.Unmarshal(raw, &name) == nil {
				parents[name] = chain
				continue
			}
			var node Group
			if err := json.Unmarshal(raw, &node); err != nil {
				return err
			}
			g := groups[node.UUID]
			if g == nil {
				// Most .bbmodel files keep the groups in the outliner itself.
				g = &node
			}
			// Copy the chain: siblings must not share the backing array, or appending to
			// one branch would corrupt the parents of another.
			next := append(append([]*Group(nil), chain...), g)
			if err := walk(node.Children, next); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(m.Outliner, nil); err != nil {
		return nil, nil, err
	}

	elements := make([]*Element, 0, len(m.Elements))
	for _, e := range m.Elements {
		if t := e.Type; t != "" && t != "cube" {
			continue
		}
		e.Groups = parents[e.UUID]
		elements = append(elements, e)
	}
	return elements, textures, nil
}

func (e *Element) box() (V3, V3) {
	lo, hi := e.From, e.To
	for i := range 3 {
		lo[i] -= e.Inflate
		hi[i] += e.Inflate
	}
	return lo, hi
}

func (e *Element) size() V3 {
	lo, hi := e.box()
	return V3{hi[0] - lo[0], hi[1] - lo[1], hi[2] - lo[2]}
}

func isRotated(r V3) bool {
	return math.Abs(r[0]) > eps || math.Abs(r[1]) > eps || math.Abs(r[2]) > eps
}

func rotMatrix(deg V3) [3][3]float64 {
	x, y, z := deg[0]*math.Pi/180, deg[1]*math.Pi/180, deg[2]*math.Pi/180
	rx := [3][3]float64{{1, 0, 0}, {0, math.Cos(x), -math.Sin(x)}, {0, math.Sin(x), math.Cos(x)}}
	ry := [3][3]float64{{math.Cos(y), 0, math.Sin(y)}, {0, 1, 0}, {-math.Sin(y), 0, math.Cos(y)}}
	rz := [3][3]float64{{math.Cos(z), -math.Sin(z), 0}, {math.Sin(z), math.Cos(z), 0}, {0, 0, 1}}
	return matMul(rz, matMul(ry, rx)) // Blockbench applies X, then Y, then Z
}

func matMul(a, b [3][3]float64) [3][3]float64 {
	var out [3][3]float64
	for i := range 3 {
		for j := range 3 {
			for k := range 3 {
				out[i][j] += a[i][k] * b[k][j]
			}
		}
	}
	return out
}

func applyM(m [3][3]float64, p, origin V3) V3 {
	// world = m·(p - origin) + origin
	var out V3
	for i := range 3 {
		out[i] = origin[i]
		for k := range 3 {
			out[i] += m[i][k] * (p[k] - origin[k])
		}
	}
	return out
}

// transform is the cube rotation, then every parent group rotation, innermost first,
// folded into one transform.
func (e *Element) transform() *Transform {
	if e.xf != nil {
		return e.xf
	}
	m := [3][3]float64{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}
	t := V3{}
	objs := make([]any, 0, len(e.Groups)+1)
	objs = append(objs, e)
	for i := len(e.Groups) - 1; i >= 0; i-- {
		objs = append(objs, e.Groups[i])
	}
	for _, obj := range objs {
		var rot, orig V3
		switch o := obj.(type) {
		case *Element:
			rot, orig = o.Rotation, o.Origin
		case *Group:
			rot, orig = o.Rotation, o.Origin
		}
		if !isRotated(rot) {
			continue
		}
		r := rotMatrix(rot)
		m = matMul(r, m)
		t = applyM(r, t, orig)
	}
	e.xf = &Transform{m, t}
	return e.xf
}

func (e *Element) toWorld(p V3) V3 {
	xf := e.transform()
	var out V3
	for i := range 3 {
		out[i] = xf.T[i]
		for k := range 3 {
			out[i] += xf.M[i][k] * p[k]
		}
	}
	return out
}

// ToLocal is the inverse of ToWorld: the rotation is orthonormal, so its inverse is its
// transpose.
func (e *Element) toLocal(p V3) V3 {
	m, t := e.transform().M, e.transform().T
	var q V3
	for i := range 3 {
		q[i] = p[i] - t[i]
	}
	var out V3
	for i := range 3 {
		out[i] = m[0][i]*q[0] + m[1][i]*q[1] + m[2][i]*q[2]
	}
	return out
}

// WorldBox is the axis-aligned box of the rotated cube.
func (e *Element) WorldBox() (V3, V3) {
	if !e.hasWB {
		lo, hi := e.box()
		first := true
		for _, x := range [2]float64{lo[0], hi[0]} {
			for _, y := range [2]float64{lo[1], hi[1]} {
				for _, z := range [2]float64{lo[2], hi[2]} {
					p := e.toWorld(V3{x, y, z})
					if first {
						e.wb[0], e.wb[1], first = p, p, false
						continue
					}
					for i := range 3 {
						e.wb[0][i] = math.Min(e.wb[0][i], p[i])
						e.wb[1][i] = math.Max(e.wb[1][i], p[i])
					}
				}
			}
		}
		e.hasWB = true
	}
	return e.wb[0], e.wb[1]
}

func (e *Element) faceDims() map[string][2]float64 {
	lo, hi := e.box()
	w, h, d := hi[0]-lo[0], hi[1]-lo[1], hi[2]-lo[2]
	return map[string][2]float64{
		"north": {w, h}, "south": {w, h}, "east": {d, h},
		"west": {d, h}, "up": {w, d}, "down": {w, d},
	}
}

// FaceCorners returns the corners (top-left, top-right, bottom-left) of each face as its
// texture lies on it.
func (e *Element) faceCorners() map[string][3]V3 {
	lo, hi := e.box()
	x0, y0, z0 := lo[0], lo[1], lo[2]
	x1, y1, z1 := hi[0], hi[1], hi[2]
	return map[string][3]V3{
		"north": {{x1, y1, z0}, {x0, y1, z0}, {x1, y0, z0}},
		"south": {{x0, y1, z1}, {x1, y1, z1}, {x0, y0, z1}},
		"east":  {{x1, y1, z1}, {x1, y1, z0}, {x1, y0, z1}},
		"west":  {{x0, y1, z0}, {x0, y1, z1}, {x0, y0, z0}},
		"up":    {{x0, y1, z0}, {x1, y1, z0}, {x0, y1, z1}},
		"down":  {{x0, y0, z1}, {x1, y0, z1}, {x0, y0, z0}},
	}
}

// TexelSize is the size of a face's texture region in texture pixels, as the face is
// oriented.
func texelSize(textures []*Texture, face *Face) (int, int) {
	if face == nil || face.Texture == nil || face.UV == nil {
		return 1, 1
	}
	t := textures[*face.Texture]
	u0, v0, u1, v1 := face.UV[0], face.UV[1], face.UV[2], face.UV[3]
	w := max(1, int(math.Round(math.Abs(u1-u0)*t.ScaleX)))
	h := max(1, int(math.Round(math.Abs(v1-v0)*t.ScaleY)))
	if int(face.Rotation)%180 == 0 {
		return w, h
	}
	return h, w
}

// FaceImage resamples a face texture to w x h pixels, oriented as Blockbench shows it.
func faceImage(textures []*Texture, face *Face, w, h int) *Img {
	if face == nil || face.Texture == nil || w <= 0 || h <= 0 {
		return nil
	}
	t := textures[*face.Texture]
	u0, v0, u1, v1 := face.UV[0], face.UV[1], face.UV[2], face.UV[3]
	rot := int(face.Rotation) % 360
	if rot < 0 {
		rot += 360
	}
	sw, sh := w, h
	if rot != 0 && rot != 180 {
		sw, sh = h, w
	}
	out := TransformExtent(t.Img, sw, sh, u0*t.ScaleX, v0*t.ScaleY, u1*t.ScaleX, v1*t.ScaleY)
	if rot != 0 {
		switch rot {
		case 90:
			out = Rotate90(out, 3)
		case 180:
			out = Rotate90(out, 2)
		case 270:
			out = Rotate90(out, 1)
		}
	}
	return out
}

func sample(textures []*Texture, face *Face, wMM, hMM, pxMM float64, turn bool) *Img {
	img := faceImage(textures, face, max(1, int(math.Round(wMM*pxMM))), max(1, int(math.Round(hMM*pxMM))))
	if img != nil && turn {
		img = Rotate90(img, 2)
	}
	return img
}

func seeThrough(img *Img) bool {
	if img == nil {
		return false
	}
	lo, _ := GrayExtrema(GrayAlpha(img))
	return lo < ALPHACutoff
}

// ClearCube is a solid cube with see-through texture somewhere: an outer layer, a cage,
// glass.
func clearCube(e *Element, textures []*Texture) bool {
	dims := e.faceDims()
	sizes := e.size()
	if math.Min(sizes[0], math.Min(sizes[1], sizes[2])) < layerMin {
		return false
	}
	for name := range dims {
		face := e.Faces[name]
		w, h := texelSize(textures, face)
		if seeThrough(faceImage(textures, face, w, h)) {
			return true
		}
	}
	return false
}

// Wrapped is the biggest cube that e's box encloses, if e is an outer layer around it.
func (e *Element) wrapped(others []*Element, tol float64) *Element {
	lo, hi := e.WorldBox()
	var best *Element
	bestVol := 0.0
	for _, o := range others {
		if o == e || o.Over != nil {
			continue
		}
		olo, ohi := o.WorldBox()
		inside := true
		for i := range 3 {
			if !(lo[i]-tol <= olo[i] && ohi[i] <= hi[i]+tol) {
				inside = false
				break
			}
		}
		if !inside {
			continue
		}
		s := o.size()
		if math.Min(s[0], math.Min(s[1], s[2])) < layerMin {
			continue
		}
		v := s[0] * s[1] * s[2]
		if best == nil || v > bestVol {
			best, bestVol = o, v
		}
	}
	return best
}

// HiddenInside reports whether a cube is fully enclosed by another opaque cube of the
// same group (so it is never seen). Cubes in `clear` hide nothing.
func HiddenInside(e *Element, others []*Element, clear map[*Element]bool) bool {
	lo, hi := e.box()
	for _, o := range others {
		if o == e || clear[o] || !sameGroups(o.Groups, e.Groups) || isRotated(o.Rotation) || isRotated(e.Rotation) {
			continue
		}
		olo, ohi := o.box()
		inside := true
		for i := range 3 {
			if !(olo[i] <= lo[i]+eps && hi[i] <= ohi[i]+eps) {
				inside = false
				break
			}
		}
		if inside && (lo != olo || hi != ohi) {
			return true
		}
	}
	return false
}

func sameGroups(a, b []*Group) bool {
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

func vSub(a, b V3) V3 { return V3{a[0] - b[0], a[1] - b[1], a[2] - b[2]} }
func vAdd(a, b V3) V3 { return V3{a[0] + b[0], a[1] + b[1], a[2] + b[2]} }
func vMul(a V3, k float64) V3 {
	return V3{a[0] * k, a[1] * k, a[2] * k}
}
func vDot(a, b V3) float64 { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }
func vCross(a, b V3) V3 {
	return V3{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}
func vUnit(a V3) V3 {
	n := math.Sqrt(vDot(a, a))
	if n == 0 {
		n = 1
	}
	return V3{a[0] / n, a[1] / n, a[2] / n}
}
func vDist(a, b V3) float64 { return math.Sqrt(vDot(vSub(a, b), vSub(a, b))) }

func key3(p V3) V3 {
	return V3{math.Round(p[0]*1e5) / 1e5, math.Round(p[1]*1e5) / 1e5, math.Round(p[2]*1e5) / 1e5}
}

func (e *Element) clipToFocus(focus [2]V3) bool {
	lo, hi := e.WorldBox()
	for i := range 3 {
		if lo[i] > focus[1][i] || hi[i] < focus[0][i] {
			return false
		}
	}
	return true
}

func fmtV3(v V3) string { return fmt.Sprintf("(%g %g %g)", v[0], v[1], v[2]) }
