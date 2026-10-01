package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/png"
	"strings"
	"time"

	"skinbot/papercraft"
)

// Generator turns skins and Blockbench models into papercraft pages, at most `jobs` at
// a time, running entirely in-process.
type Generator struct {
	credit string
	slots  chan struct{}
}

type Result struct {
	PDF      []byte   // only when the PDF was asked for
	PNGs     [][]byte // one per page, only when PNGs or a preview were asked for
	Model    string   // steve or alex, as actually used (skins only)
	Detailed bool     // a Blockbench model has pieces too small to cut out comfortably
}

func NewGenerator(credit string, jobs int) *Generator {
	return &Generator{credit: credit, slots: make(chan struct{}, max(jobs, 1))}
}

// What a generator run has to produce. Encoding pages is a good part of the work, so only
// the asked-for format is made, and a preview stops after the first page.
const (
	wantPDF     = "pdf"
	wantPNG     = "png"
	wantPreview = "preview"
)

// errEnough stops rendering once a preview has its first page.
var errEnough = errors.New("enough pages")

// Run makes a papercraft of a craft of either kind.
func (g *Generator) Run(ctx context.Context, kind string, data []byte, model, layers, want string) (*Result, error) {
	if kind == kindModel {
		return g.RunModel(ctx, data, want)
	}
	return g.RunSkin(ctx, data, model, layers, want)
}

func (g *Generator) RunSkin(ctx context.Context, skinPNG []byte, model, layers, want string) (*Result, error) {
	release, err := g.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	skin, err := papercraft.LoadSkin(skinPNG)
	if err != nil {
		return nil, err
	}
	var forceSlim *bool
	switch model {
	case "steve":
		v := false
		forceSlim = &v
	case "alex":
		v := true
		forceSlim = &v
	}
	out := &Result{}
	pdf := papercraft.NewPdfWriter(300)
	emit := pageEncoder(ctx, out, pdf, want, 6)
	slim, _, err := papercraft.RenderSkin(skin, 2.5, 300, layers, forceSlim, g.credit, emit)
	if errors.Is(err, errEnough) {
		return out, nil
	}
	if err != nil {
		return nil, timeoutErr(ctx, err)
	}
	out.Model = "steve"
	if slim {
		out.Model = "alex"
	}
	return out, finishPDF(out, pdf, want, ctx)
}

// RunModel turns a Blockbench model into a papercraft. Big models take a while: every
// pair of cubes that sink into each other is cut apart first.
func (g *Generator) RunModel(ctx context.Context, model []byte, want string) (*Result, error) {
	release, err := g.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	out := &Result{}
	pdf := papercraft.NewPdfWriter(300)
	emit := pageEncoder(ctx, out, pdf, want, 3)
	_, log, err := papercraft.RenderModel(model, 8, 300, g.credit, emit)
	if errors.Is(err, errEnough) {
		return out, nil
	}
	if err != nil {
		return nil, timeoutErr(ctx, err)
	}
	out.Detailed = strings.Contains(log, "too detailed for A4")
	return out, finishPDF(out, pdf, want, ctx)
}

func (g *Generator) acquire(ctx context.Context) (func(), error) {
	select {
	case g.slots <- struct{}{}:
		return func() { <-g.slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// pageEncoder turns each finished page into the PNG bytes the bot sends or appends the
// lossless page to the PDF.
func pageEncoder(ctx context.Context, res *Result, pdf *papercraft.PdfWriter, want string, level int) func(*papercraft.Img) error {
	return func(page *papercraft.Img) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if want == wantPDF {
			pdf.Add(page)
			return nil
		}
		data, err := papercraft.SavePNG(page, 300, level)
		if err != nil {
			return err
		}
		res.PNGs = append(res.PNGs, data)
		if want == wantPreview {
			return errEnough
		}
		return nil
	}
}

func finishPDF(res *Result, pdf *papercraft.PdfWriter, want string, ctx context.Context) error {
	if want != wantPDF {
		return timeoutErr(ctx, nil)
	}
	var buf bytes.Buffer
	if err := pdf.Write(&buf); err != nil {
		return err
	}
	res.PDF = buf.Bytes()
	return timeoutErr(ctx, nil)
}

func timeoutErr(ctx context.Context, err error) error {
	if err == nil && ctx.Err() == nil {
		return nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errTooSlow
	}
	if err != nil {
		return err
	}
	return ctx.Err()
}

// validSkin checks that data is a PNG with Minecraft skin proportions.
func validSkin(data []byte) bool {
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return false
	}
	w, h := cfg.Width, cfg.Height
	return w >= 64 && w <= 1024 && w%64 == 0 && (h == w || h == w/2)
}

var (
	errTooSlow     = errors.New("generator timed out")
	errOutOfMemory = errors.New("generator killed, out of memory")
)

// Reasons a Blockbench file cannot be turned into a papercraft.
var (
	errNotModel      = errors.New("not a Blockbench model")
	errNoCubes       = errors.New("model has no cubes")
	errMesh          = errors.New("model has meshes")
	errNoTexture     = errors.New("model has no textures")
	errLinkedTexture = errors.New("texture is not embedded")
)

// checkModel looks at a .bbmodel file before it is queued and returns the number of cubes.
func checkModel(data []byte) (int, error) {
	var m struct {
		Meta     *json.RawMessage `json:"meta"`
		Elements []struct {
			Type string `json:"type"`
		} `json:"elements"`
		Textures []struct {
			Source string `json:"source"`
		} `json:"textures"`
	}
	if err := json.Unmarshal(data, &m); err != nil || m.Meta == nil {
		return 0, errNotModel
	}
	cubes := 0
	for _, e := range m.Elements {
		switch e.Type {
		case "", "cube":
			cubes++
		case "mesh":
			return 0, errMesh
		}
	}
	if cubes == 0 {
		return 0, errNoCubes
	}
	if len(m.Textures) == 0 {
		return 0, errNoTexture
	}
	for _, t := range m.Textures {
		if !strings.HasPrefix(t.Source, "data:image/") {
			return 0, errLinkedTexture
		}
	}
	return cubes, nil
}
