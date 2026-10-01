// Package papercraft turns Minecraft skins and Blockbench models into printable
// papercraft nets (A4, PNG + PDF). It is a Go port of skin_papercraft.py and
// bbmodel_papercraft.py.
package papercraft

import (
	"image"
	"image/color"
	"math"
)

// Img is a straight-alpha RGBA image, the equivalent of PIL's "RGBA" mode.
type Img = image.NRGBA

// Gray is the equivalent of PIL's "L" mode.
type Gray = image.Gray

func NewImg(w, h int, c color.NRGBA) *Img {
	im := image.NewNRGBA(image.Rect(0, 0, w, h))
	if c != (color.NRGBA{}) {
		Fill(im, c)
	}
	return im
}

func NewGray(r image.Rectangle) *Gray {
	return image.NewGray(r)
}

// FillGray paints the whole mask with v.
func FillGray(im *Gray, v uint8) {
	for i := range im.Pix {
		im.Pix[i] = v
	}
}

func imageRect(w, h int) image.Rectangle {
	return image.Rect(0, 0, w, h)
}

func Clone(im *Img) *Img {
	out := image.NewNRGBA(im.Bounds())
	copy(out.Pix, im.Pix)
	return out
}

func CloneGray(im *Gray) *Gray {
	out := image.NewGray(im.Bounds())
	copy(out.Pix, im.Pix)
	return out
}

func Fill(im *Img, c color.NRGBA) {
	for y := 0; y < im.Rect.Dy(); y++ {
		row := im.Pix[y*im.Stride : y*im.Stride+im.Rect.Dx()*4]
		for x := 0; x < len(row); x += 4 {
			row[x], row[x+1], row[x+2], row[x+3] = c.R, c.G, c.B, c.A
		}
	}
}

// FillRect paints an inclusive rectangle, like PIL's draw.rectangle.
func FillRect(im *Img, x0, y0, x1, y1 int, c color.NRGBA) {
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	r := image.Rect(x0, y0, x1+1, y1+1).Intersect(im.Bounds())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := im.Pix[y*im.Stride+r.Min.X*4 : y*im.Stride+r.Max.X*4]
		for x := 0; x < len(row); x += 4 {
			row[x], row[x+1], row[x+2], row[x+3] = c.R, c.G, c.B, c.A
		}
	}
}

func FillRectGray(im *Gray, x0, y0, x1, y1 int, v uint8) {
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	r := image.Rect(x0, y0, x1+1, y1+1).Intersect(im.Bounds())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			im.Pix[y*im.Stride+x] = v
		}
	}
}

// Paste copies src pixels; the equivalent of PIL's im.paste(src, box) without mask.
func Paste(dst *Img, src *Img, x, y int) {
	PasteMask(dst, src, x, y, nil)
}

// PasteMask is PIL's im.paste(src, box, mask): mask 255 keeps src, 0 keeps dst.
func PasteMask(dst *Img, src *Img, x, y int, mask *Gray) {
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
			si := sy*src.Stride + sx*4
			di := dy*dst.Stride + dx*4
			if mask == nil {
				dst.Pix[di] = src.Pix[si]
				dst.Pix[di+1] = src.Pix[si+1]
				dst.Pix[di+2] = src.Pix[si+2]
				dst.Pix[di+3] = src.Pix[si+3]
				continue
			}
			m := uint32(mask.Pix[sy*mask.Stride+sx])
			switch m {
			case 0: // the blend below leaves dst as it is
				continue
			case 255: // and copies src
				copy(dst.Pix[di:di+4], src.Pix[si:si+4])
				continue
			}
			for k := 0; k < 4; k++ {
				dst.Pix[di+k] = uint8((uint32(src.Pix[si+k])*m + uint32(dst.Pix[di+k])*(255-m) + 127) / 255)
			}
		}
	}
}

// AlphaComposite draws src over dst in place (PIL's Image.alpha_composite).
func AlphaComposite(dst, src *Img) {
	db := dst.Bounds()
	for y := 0; y < db.Dy(); y++ {
		for x := 0; x < db.Dx(); x++ {
			di := y*dst.Stride + x*4
			si := y*src.Stride + x*4
			sa := uint32(src.Pix[si+3])
			if sa == 0 {
				continue
			}
			if sa == 255 {
				copy(dst.Pix[di:di+4], src.Pix[si:si+4])
				continue
			}
			da := uint32(dst.Pix[di+3])
			rest := 255 - sa
			outA := sa + (da*rest+127)/255
			if outA == 0 {
				dst.Pix[di], dst.Pix[di+1], dst.Pix[di+2], dst.Pix[di+3] = 0, 0, 0, 0
				continue
			}
			for k := 0; k < 3; k++ {
				sc := uint32(src.Pix[si+k])
				dc := uint32(dst.Pix[di+k])
				num := sc*sa*255 + dc*da*rest
				dst.Pix[di+k] = uint8((num + 255*outA/2) / (255 * outA))
			}
			dst.Pix[di+3] = uint8(outA)
		}
	}
}

func Crop(im *Img, x0, y0, x1, y1 int) *Img {
	r := image.Rect(x0, y0, x1, y1).Intersect(im.Bounds())
	out := image.NewNRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	for y := 0; y < r.Dy(); y++ {
		copy(out.Pix[y*out.Stride:(y+1)*out.Stride], im.Pix[(r.Min.Y+y)*im.Stride+r.Min.X*4:])
	}
	return out
}

func FlipH(im *Img) *Img {
	out := image.NewNRGBA(im.Bounds())
	w, h := im.Rect.Dx(), im.Rect.Dy()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			si := y*im.Stride + x*4
			di := y*out.Stride + (w-1-x)*4
			copy(out.Pix[di:di+4], im.Pix[si:si+4])
		}
	}
	return out
}

func FlipV(im *Img) *Img {
	out := image.NewNRGBA(im.Bounds())
	_, h := im.Rect.Dx(), im.Rect.Dy()
	for y := 0; y < h; y++ {
		copy(out.Pix[y*out.Stride:(y+1)*out.Stride], im.Pix[(h-1-y)*im.Stride:(h-y)*im.Stride])
	}
	return out
}

// ResizeNearest scales with nearest-neighbour sampling; a pixel centre maps back to the
// source, like PIL's Image.resize(..., NEAREST).
func ResizeNearest(im *Img, w, h int) *Img {
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	sw, sh := im.Rect.Dx(), im.Rect.Dy()
	for y := 0; y < h; y++ {
		sy := clampInt(int((float64(y)+0.5)*float64(sh)/float64(h)), 0, sh-1)
		for x := 0; x < w; x++ {
			sx := clampInt(int((float64(x)+0.5)*float64(sw)/float64(w)), 0, sw-1)
			copy(out.Pix[y*out.Stride+x*4:], im.Pix[sy*im.Stride+sx*4:sy*im.Stride+sx*4+4])
		}
	}
	return out
}

// Rotate90 returns the image rotated counter-clockwise by 90°*n, with the frame growing
// like PIL's Image.rotate(90*n, expand=True).
func Rotate90(im *Img, n int) *Img {
	n = ((n % 4) + 4) % 4
	w, h := im.Rect.Dx(), im.Rect.Dy()
	if n == 0 {
		return Clone(im)
	}
	var out *Img
	if n == 2 {
		out = image.NewNRGBA(image.Rect(0, 0, w, h))
	} else {
		out = image.NewNRGBA(image.Rect(0, 0, h, w))
	}
	ow, oh := out.Rect.Dx(), out.Rect.Dy()
	for y := 0; y < oh; y++ {
		row := out.Pix[y*out.Stride : y*out.Stride+ow*4]
		// Source of dst(0, y) and the step to the source of dst(x+1, y).
		var si, step int
		switch n {
		case 1: // CCW: dst(x, y) comes from src(w-1-y, x)
			si, step = w-1-y, im.Stride
			si *= 4
		case 2:
			si, step = (h-1-y)*im.Stride+(w-1)*4, -4
		case 3: // CW: dst(x, y) comes from src(y, h-1-x)
			si, step = (h-1)*im.Stride+y*4, -im.Stride
		}
		for x := 0; x < ow*4; x += 4 {
			p := im.Pix[si : si+4 : si+4]
			row[x], row[x+1], row[x+2], row[x+3] = p[0], p[1], p[2], p[3]
			si += step
		}
	}
	return out
}

// RotateGray90 is Rotate90 for a one-channel image.
func RotateGray90(im *Gray, n int) *Gray {
	n = ((n % 4) + 4) % 4
	w, h := im.Rect.Dx(), im.Rect.Dy()
	out := image.NewGray(image.Rect(0, 0, w, h))
	if n%2 == 1 {
		out = image.NewGray(image.Rect(0, 0, h, w))
	}
	ow, oh := out.Rect.Dx(), out.Rect.Dy()
	for y := 0; y < oh; y++ {
		row := out.Pix[y*out.Stride : y*out.Stride+ow]
		var si, step int
		switch n {
		case 0:
			si, step = y*im.Stride, 1
		case 1:
			si, step = w-1-y, im.Stride
		case 2:
			si, step = (h-1-y)*im.Stride+w-1, -1
		case 3:
			si, step = (h-1)*im.Stride+y, -im.Stride
		}
		for x := range row {
			row[x] = im.Pix[si]
			si += step
		}
	}
	return out
}

// TransformExtent resamples the rectangle box (x0, y0, x1, y1) of src onto a w×h image,
// like PIL's Image.transform((w, h), EXTENT, box, NEAREST).
func TransformExtent(src *Img, w, h int, x0, y0, x1, y1 float64) *Img {
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	ux, uy := (x1-x0)/float64(w), (y1-y0)/float64(h)
	for y := 0; y < h; y++ {
		sy := clampInt(int(math.Floor(y0+(float64(y)+0.5)*uy)), 0, sh-1)
		for x := 0; x < w; x++ {
			sx := clampInt(int(math.Floor(x0+(float64(x)+0.5)*ux)), 0, sw-1)
			copy(out.Pix[y*out.Stride+x*4:], src.Pix[sy*src.Stride+sx*4:sy*src.Stride+sx*4+4])
		}
	}
	return out
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// GrayAlpha extracts the alpha channel (PIL's getchannel("A")).
func GrayAlpha(im *Img) *Gray {
	out := image.NewGray(im.Bounds())
	for y := 0; y < im.Rect.Dy(); y++ {
		for x := 0; x < im.Rect.Dx(); x++ {
			out.Pix[y*out.Stride+x] = im.Pix[y*im.Stride+x*4+3]
		}
	}
	return out
}

func GrayExtrema(im *Gray) (uint8, uint8) {
	lo, hi := uint8(255), uint8(0)
	for _, v := range im.Pix {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	return lo, hi
}

// PutAlpha overwrites the alpha channel (PIL's putalpha).
func PutAlpha(im *Img, a uint8) {
	for y := 0; y < im.Rect.Dy(); y++ {
		for x := 0; x < im.Rect.Dx(); x++ {
			im.Pix[y*im.Stride+x*4+3] = a
		}
	}
}

func GraySubtract(a, b *Gray) *Gray {
	out := image.NewGray(a.Bounds())
	for i, v := range a.Pix {
		if d := int(v) - int(b.Pix[i]); d > 0 {
			out.Pix[i] = uint8(d)
		}
	}
	return out
}

func GrayMultiply(a, b *Gray) *Gray {
	out := image.NewGray(a.Bounds())
	for i, v := range a.Pix {
		out.Pix[i] = uint8((uint32(v)*uint32(b.Pix[i]) + 127) / 255)
	}
	return out
}

// MaxFilter replaces every pixel with the maximum of its (2r+1)² window (PIL MaxFilter).
func MaxFilter(im *Gray, r int) *Gray {
	return rankFilter(im, r, true)
}

// MinFilter replaces every pixel with the minimum of its (2r+1)² window (PIL MinFilter).
func MinFilter(im *Gray, r int) *Gray {
	return rankFilter(im, r, false)
}

func rankFilter(im *Gray, r int, maximum bool) *Gray {
	w, h := im.Rect.Dx(), im.Rect.Dy()
	out := image.NewGray(im.Bounds())
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			best := im.Pix[y*im.Stride+x]
			if !maximum {
				best = 255
			}
			for dy := -r; dy <= r; dy++ {
				yy := clampInt(y+dy, 0, h-1)
				for dx := -r; dx <= r; dx++ {
					xx := clampInt(x+dx, 0, w-1)
					v := im.Pix[yy*im.Stride+xx]
					if (maximum && v > best) || (!maximum && v < best) {
						best = v
					}
				}
			}
			out.Pix[y*out.Stride+x] = best
		}
	}
	return out
}

// Reduce averages f×f blocks, rounding to nearest (PIL's Image.reduce).
func Reduce(im *Gray, f int) *Gray {
	w, h := im.Rect.Dx(), im.Rect.Dy()
	out := image.NewGray(image.Rect(0, 0, (w+f-1)/f, (h+f-1)/f))
	for y := 0; y < out.Rect.Dy(); y++ {
		for x := 0; x < out.Rect.Dx(); x++ {
			sum, n := 0, 0
			for dy := 0; dy < f; dy++ {
				for dx := 0; dx < f; dx++ {
					xx, yy := x*f+dx, y*f+dy
					if xx >= w || yy >= h {
						continue
					}
					sum += int(im.Pix[yy*im.Stride+xx])
					n++
				}
			}
			if n > 0 {
				out.Pix[y*out.Stride+x] = uint8(min((sum+n/2)/n, 255))
			}
		}
	}
	return out
}

// Polygon fills a polygon with the even-odd rule, like PIL's draw.polygon.
type PointF struct{ X, Y float64 }

func Polygon(im *Img, pts []PointF, c color.NRGBA) {
	if len(pts) < 3 {
		return
	}
	minY, maxY := math.Inf(1), math.Inf(-1)
	for _, p := range pts {
		minY, maxY = math.Min(minY, p.Y), math.Max(maxY, p.Y)
	}
	y0 := clampInt(int(math.Floor(minY)), 0, im.Rect.Dy()-1)
	y1 := clampInt(int(math.Ceil(maxY)), 0, im.Rect.Dy()-1)
	cross := make([]float64, 0, len(pts))
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
				SetPixel(im, x, y, c)
			}
		}
	}
}

func SetPixel(im *Img, x, y int, c color.NRGBA) {
	if !(image.Point{x, y}.In(im.Bounds())) {
		return
	}
	i := y*im.Stride + x*4
	im.Pix[i], im.Pix[i+1], im.Pix[i+2], im.Pix[i+3] = c.R, c.G, c.B, c.A
}

func GetPixel(im *Img, x, y int) color.NRGBA {
	i := y*im.Stride + x*4
	return color.NRGBA{im.Pix[i], im.Pix[i+1], im.Pix[i+2], im.Pix[i+3]}
}

// Line draws a thick segment with flat caps, plus round joints when a polyline is given.
func Line(im *Img, pts []PointF, c color.NRGBA, width float64, jointRound bool) {
	if width < 1 {
		width = 1
	}
	if len(pts) == 1 {
		Dot(im, pts[0], width/2, c)
		return
	}
	half := width / 2
	for i := 0; i+1 < len(pts); i++ {
		seg(im, pts[i], pts[i+1], c, half)
	}
	if jointRound {
		for i := 1; i+1 < len(pts); i++ {
			Dot(im, pts[i], half, c)
		}
	}
}

func seg(im *Img, a, b PointF, c color.NRGBA, half float64) {
	dx, dy := b.X-a.X, b.Y-a.Y
	length := math.Hypot(dx, dy)
	if length == 0 {
		Dot(im, a, half, c)
		return
	}
	nx, ny := -dy/length*half, dx/length*half
	poly := []PointF{
		{a.X + nx, a.Y + ny}, {b.X + nx, b.Y + ny},
		{b.X - nx, b.Y - ny}, {a.X - nx, a.Y - ny},
	}
	Polygon(im, poly, c)
}

func Dot(im *Img, p PointF, r float64, c color.NRGBA) {
	Polygon(im, circle(p, r), c)
}

func circle(p PointF, r float64) []PointF {
	pts := make([]PointF, 0, 16)
	for i := 0; i < 16; i++ {
		a := 2 * math.Pi * float64(i) / 16
		pts = append(pts, PointF{p.X + r*math.Cos(a), p.Y + r*math.Sin(a)})
	}
	return pts
}

// Ellipse draws a filled ellipse with an outline ring, like PIL's draw.ellipse.
func Ellipse(im *Img, x0, y0, x1, y1 float64, fill, outline color.NRGBA, width float64) {
	if width < 1 {
		width = 1
	}
	cx, cy := (x0+x1)/2, (y0+y1)/2
	rx, ry := (x1-x0)/2, (y1-y0)/2
	if outline.A != 0 {
		EllipseFill(im, cx, cy, rx, ry, outline)
		if fill.A != 0 {
			EllipseFill(im, cx, cy, rx-width, ry-width, fill)
		}
		return
	}
	if fill.A != 0 {
		EllipseFill(im, cx, cy, rx, ry, fill)
	}
}

// EllipseFill rasterises a filled ellipse without anti-aliasing.
func EllipseFill(im *Img, cx, cy, rx, ry float64, c color.NRGBA) {
	if rx <= 0 || ry <= 0 {
		return
	}
	y0 := clampInt(int(math.Floor(cy-ry)), 0, im.Rect.Dy()-1)
	y1 := clampInt(int(math.Ceil(cy+ry)), 0, im.Rect.Dy()-1)
	for y := y0; y <= y1; y++ {
		dy := (float64(y) + 0.5 - cy) / ry
		if dy < -1 || dy > 1 {
			continue
		}
		dx := rx * math.Sqrt(1-dy*dy)
		x0 := clampInt(int(math.Ceil(cx-dx-0.5)), 0, im.Rect.Dx()-1)
		x1 := clampInt(int(math.Ceil(cx+dx-0.5))-1, 0, im.Rect.Dx()-1)
		for x := x0; x <= x1; x++ {
			SetPixel(im, x, y, c)
		}
	}
}

// DashedLine draws a dashed line of the given width; dashes are `dash` long.
func DashedLine(im *Img, p0, p1 PointF, width, dash float64, c color.NRGBA) {
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
		Line(im, []PointF{
			{p0.X + (p1.X-p0.X)*a, p0.Y + (p1.Y-p0.Y)*a},
			{p0.X + (p1.X-p0.X)*b, p0.Y + (p1.Y-p0.Y)*b},
		}, c, width, false)
	}
}
