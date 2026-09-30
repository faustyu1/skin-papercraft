package papercraft

import (
	"image"
	"image/color"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Font is a TrueType font at a pixel size, with PIL-style anchors.
type Font struct {
	face    font.Face
	ascent  float64
	descent float64
}

var (
	fontOnce sync.Once
	fontData *opentype.Font
	fontErr  error
)

func NewFont(size float64) *Font {
	fontOnce.Do(func() {
		fontData, fontErr = opentype.Parse(gobold.TTF)
	})
	if fontErr != nil {
		panic(fontErr)
	}
	face, err := opentype.NewFace(fontData, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		panic(err)
	}
	m := face.Metrics()
	return &Font{face: face, ascent: float64(m.Ascent) / 64, descent: float64(m.Descent) / 64}
}

func (f *Font) Close() {
	_ = f.face.Close()
}

// Width is the advance width of s in pixels, like PIL's draw.textlength.
func (f *Font) Width(s string) float64 {
	return float64(font.MeasureString(f.face, s)) / 64
}

// Draw renders s at (x, y). Anchor is PIL's two-letter form: horizontal l/m, vertical t/m.
func (f *Font) Draw(dst *Img, x, y float64, s, anchor string, c color.NRGBA) {
	ox := x
	if anchor[0] == 'm' {
		ox -= f.Width(s) / 2
	}
	baseline := y + f.ascent
	if anchor[1] == 'm' {
		baseline = y + (f.ascent-f.descent)/2
	}
	d := font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(c),
		Face: f.face,
		Dot:  fixed.P(int(ox+0.5), int(baseline+0.5)),
	}
	d.DrawString(s)
}
