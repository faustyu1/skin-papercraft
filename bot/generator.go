package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Generator runs skin_papercraft.py and bbmodel_papercraft.py, at most `jobs` at a time.
type Generator struct {
	python, skinScript, modelScript, credit string
	slots                                   chan struct{}
}

type Result struct {
	PDF      []byte
	PNGs     [][]byte // one per page
	Model    string   // steve or alex, as actually used (skins only)
	Detailed bool     // a Blockbench model has pieces too small to cut out comfortably
}

func NewGenerator(python, skinScript, modelScript, credit string, jobs int) *Generator {
	return &Generator{python: python, skinScript: skinScript, modelScript: modelScript, credit: credit,
		slots: make(chan struct{}, max(jobs, 1))}
}

// Run makes a papercraft of a craft of either kind.
func (g *Generator) Run(ctx context.Context, kind string, data []byte, model, layers string) (*Result, error) {
	if kind == kindModel {
		return g.RunModel(ctx, data)
	}
	return g.RunSkin(ctx, data, model, layers)
}

func (g *Generator) RunSkin(ctx context.Context, skin []byte, model, layers string) (*Result, error) {
	res, log, err := g.run(ctx, skin, "skin.png", 90*time.Second, g.skinScript, "--model", model, "--layers", layers)
	if err != nil {
		return nil, err
	}
	res.Model = "steve"
	if bytes.Contains(log, []byte(": alex model")) {
		res.Model = "alex"
	}
	return res, nil
}

// RunModel turns a Blockbench model into a papercraft. Big models take a while: every
// pair of cubes that sink into each other is cut apart first.
func (g *Generator) RunModel(ctx context.Context, model []byte) (*Result, error) {
	res, log, err := g.run(ctx, model, "model.bbmodel", 3*time.Minute, g.modelScript)
	if err != nil {
		return nil, err
	}
	res.Detailed = bytes.Contains(log, []byte("too detailed for A4"))
	return res, nil
}

// run writes the input as `name` and runs the script on it; the script writes
// <stem>_papercraft.pdf and <stem>_papercraft[_pageN].png.
func (g *Generator) run(ctx context.Context, input []byte, name string, timeout time.Duration,
	script string, args ...string) (*Result, []byte, error) {
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}

	dir, err := os.MkdirTemp("", "papercraft-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)

	in, out := filepath.Join(dir, name), filepath.Join(dir, "out")
	if err := os.WriteFile(in, input, 0o600); err != nil {
		return nil, nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args = append([]string{script, in, "--credit", g.credit, "--out-dir", out}, args...)
	log, err := exec.CommandContext(ctx, g.python, args...).CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, nil, errTooSlow
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && strings.Contains(exit.Error(), "signal: killed") {
		// Not our timeout, so the kernel stopped it: the server ran out of memory.
		return nil, nil, fmt.Errorf("%w: %s", errOutOfMemory, log)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("generator: %w: %s", err, log)
	}

	base := filepath.Join(out, strings.TrimSuffix(name, filepath.Ext(name))+"_papercraft")
	res := &Result{}
	if res.PDF, err = os.ReadFile(base + ".pdf"); err != nil {
		return nil, nil, err
	}
	for page := 1; ; page++ {
		file := base + ".png"
		if page > 1 {
			file = fmt.Sprintf("%s_page%d.png", base, page)
		}
		data, err := os.ReadFile(file)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		res.PNGs = append(res.PNGs, data)
	}
	if len(res.PNGs) == 0 {
		return nil, nil, errors.New("generator produced no pages")
	}
	return res, log, nil
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
