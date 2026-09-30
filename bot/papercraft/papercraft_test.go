package papercraft

import (
	"bytes"
	"image/color"
	"os"
	"path/filepath"
	"testing"
)

func TestRotate90(t *testing.T) {
	im := NewImg(2, 3, color.NRGBA{})
	vals := []uint8{1, 2, 3, 4, 5, 6}
	for i, v := range vals {
		im.Pix[i*4] = v
	}
	ccw := Rotate90(im, 1)
	if ccw.Rect.Dx() != 3 || ccw.Rect.Dy() != 2 {
		t.Fatalf("size %v", ccw.Rect)
	}
	want := []uint8{2, 4, 6, 1, 3, 5}
	for i, v := range want {
		if ccw.Pix[i*4] != v {
			t.Fatalf("ccw[%d] = %d, want %d", i, ccw.Pix[i*4], v)
		}
	}
	cw := Rotate90(im, 3)
	want = []uint8{5, 3, 1, 6, 4, 2}
	for i, v := range want {
		if cw.Pix[i*4] != v {
			t.Fatalf("cw[%d] = %d, want %d", i, cw.Pix[i*4], v)
		}
	}
}

func TestPasteMask(t *testing.T) {
	dst := NewImg(1, 1, color.NRGBA{10, 20, 30, 255})
	src := NewImg(1, 1, color.NRGBA{200, 100, 50, 255})
	mask := NewGray(imageRect(1, 1))
	for m, want := range map[uint8]uint8{127: 105, 129: 106, 254: 199, 255: 200} {
		mask.Pix[0] = m
		d := Clone(dst)
		PasteMask(d, src, 0, 0, mask)
		if d.Pix[0] != want {
			t.Fatalf("mask %d: got %d, want %d", m, d.Pix[0], want)
		}
	}
}

func TestAlphaComposite(t *testing.T) {
	d := NewImg(1, 1, color.NRGBA{10, 20, 30, 100})
	s := NewImg(1, 1, color.NRGBA{200, 100, 50, 128})
	AlphaComposite(d, s)
	got := GetPixel(d, 0, 0)
	if got != (color.NRGBA{147, 78, 44, 178}) {
		t.Fatalf("got %v", got)
	}
}

func TestResizeNearest(t *testing.T) {
	im := NewImg(2, 1, color.NRGBA{})
	im.Pix[0], im.Pix[4] = 10, 20
	out := ResizeNearest(im, 4, 1)
	for i, want := range []uint8{10, 10, 20, 20} {
		if out.Pix[i*4] != want {
			t.Fatalf("out[%d] = %d, want %d", i, out.Pix[i*4], want)
		}
	}
}

func TestGenerateSkin(t *testing.T) {
	data, err := os.ReadFile("../../skin.png")
	if err != nil {
		t.Skip("../../skin.png not available")
	}
	res, err := GenerateSkin(data, 2.5, 300, "separate", "auto", "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Pages) == 0 || res.Pages[0].Rect.Dx() != 2480 {
		t.Fatalf("bad pages: %d", len(res.Pages))
	}
	if _, err := SavePNG(res.Pages[0], 300, 6); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateModel(t *testing.T) {
	paths, _ := filepath.Glob("../../*.bbmodel")
	var best []byte
	for _, p := range paths {
		if data, err := os.ReadFile(p); err == nil && (best == nil || len(data) < len(best)) {
			best = data
		}
	}
	if best == nil {
		t.Skip("no ../../*.bbmodel available")
	}
	res, err := GenerateModel(best, 8, 300, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Pages) < 2 {
		t.Fatalf("bad pages: %d", len(res.Pages))
	}
	var buf bytes.Buffer
	pdf := NewPdfWriter(300)
	for _, page := range res.Pages {
		pdf.Add(page)
	}
	if err := pdf.Write(&buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte("%PDF")) {
		t.Fatal("not a pdf")
	}
}
