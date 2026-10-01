package papercraft

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"io"
	"math"
)

// PdfWriter is a lossless PDF writer (Pillow's writer uses JPEG, which smears pixel
// art), built page by page so finished pages do not stay in memory.
type PdfWriter struct {
	dpi     int
	objects [][]byte
	kids    []string
}

func NewPdfWriter(dpi int) *PdfWriter {
	return &PdfWriter{dpi: dpi, objects: [][]byte{[]byte("<< /Type /Catalog /Pages 2 0 R >>"), nil}}
}

func (p *PdfWriter) Add(page *Img) {
	w, h := page.Rect.Dx(), page.Rect.Dy()
	pw := float64(w) * 72 / float64(p.dpi)
	ph := float64(h) * 72 / float64(p.dpi)

	// Compressed in strips: the whole page as bytes would be another 26 MB at 300 dpi.
	var buf bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&buf, 6)
	rgb := make([]byte, w*3)
	for y0 := 0; y0 < h; y0 += 256 {
		y1 := min(h, y0+256)
		for y := y0; y < y1; y++ {
			row := page.Pix[y*page.Stride : y*page.Stride+w*4]
			for x := 0; x < w; x++ {
				rgb[x*3] = row[x*4]
				rgb[x*3+1] = row[x*4+1]
				rgb[x*3+2] = row[x*4+2]
			}
			zw.Write(rgb)
		}
	}
	zw.Close()
	data := buf.Bytes()

	content := []byte(fmt.Sprintf("q %.2f 0 0 %.2f 0 0 cm /Im0 Do Q", pw, ph))
	n := len(p.objects) + 1
	p.kids = append(p.kids, fmt.Sprintf("%d 0 R", n))
	p.objects = append(p.objects,
		[]byte(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] "+
			"/Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>", pw, ph, n+1, n+2)),
		append([]byte(fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB "+
			"/BitsPerComponent 8 /Filter /FlateDecode /Length %d >>\nstream\n", w, h, len(data))), append(data, []byte("\nendstream")...)...),
		append([]byte(fmt.Sprintf("<< /Length %d >>\nstream\n", len(content))), append(content, []byte("\nendstream")...)...),
	)
}

func (p *PdfWriter) Write(w io.Writer) error {
	objects := make([][]byte, len(p.objects))
	copy(objects, p.objects)
	objects[1] = []byte(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>",
		joinStrings(p.kids, " "), len(p.kids)))

	if _, err := w.Write([]byte("%PDF-1.4\n")); err != nil {
		return err
	}
	offsets := make([]int, len(objects))
	pos := 8
	for i, obj := range objects {
		offsets[i] = pos
		header := fmt.Sprintf("%d 0 obj\n", i+1)
		if _, err := io.WriteString(w, header); err != nil {
			return err
		}
		if _, err := w.Write(obj); err != nil {
			return err
		}
		if _, err := w.Write([]byte("\nendobj\n")); err != nil {
			return err
		}
		pos += len(header) + len(obj) + len("\nendobj\n")
	}
	xref := pos
	if _, err := io.WriteString(w, fmt.Sprintf("xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)); err != nil {
		return err
	}
	for _, o := range offsets {
		if _, err := io.WriteString(w, fmt.Sprintf("%010d 00000 n \n", o)); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(objects)+1, xref))
	return err
}

func joinStrings(ss []string, sep string) string {
	var b bytes.Buffer
	for i, s := range ss {
		if i > 0 {
			b.WriteString(sep)
		}
		b.WriteString(s)
	}
	return b.String()
}

// SavePNG encodes the image with the given zlib compression level (1-9) and DPI metadata.
func SavePNG(im *Img, dpi, level int) ([]byte, error) {
	// image/png only knows its own named levels and treats any other number as the
	// default, so map zlib levels onto them.
	cl := png.DefaultCompression
	switch {
	case level <= 3:
		cl = png.BestSpeed
	case level >= 9:
		cl = png.BestCompression
	}
	enc := png.Encoder{CompressionLevel: cl}
	var buf bytes.Buffer
	if err := enc.Encode(&buf, im); err != nil {
		return nil, err
	}
	ppm := int(math.Round(float64(dpi) / 0.0254))
	return withPhys(buf.Bytes(), uint32(ppm), uint32(ppm)), nil
}

// withPhys inserts a pHYs (physical pixel size) chunk after IHDR, the way PIL stores
// the DPI on save.
func withPhys(data []byte, x, y uint32) []byte {
	if len(data) < 33 {
		return data
	}
	ihdrLen := int(binary.BigEndian.Uint32(data[8:12]))
	pos := 8 + 12 + ihdrLen
	var chunk bytes.Buffer
	binary.Write(&chunk, binary.BigEndian, uint32(9))
	chunk.WriteString("pHYs")
	binary.Write(&chunk, binary.BigEndian, x)
	binary.Write(&chunk, binary.BigEndian, y)
	chunk.WriteByte(1)
	crc := crc32.NewIEEE()
	crc.Write(chunk.Bytes()[4:]) // type + data
	binary.Write(&chunk, binary.BigEndian, crc.Sum32())

	out := make([]byte, 0, len(data)+chunk.Len())
	out = append(out, data[:pos]...)
	out = append(out, chunk.Bytes()...)
	out = append(out, data[pos:]...)
	return out
}

// GrayFromBytes builds a grayscale mask from raw bytes with row stride.
func GrayFromBytes(w, h int, pix []byte) *image.Gray {
	im := image.NewGray(image.Rect(0, 0, w, h))
	copy(im.Pix, pix)
	return im
}
