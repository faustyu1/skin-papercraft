package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Generator runs skin_papercraft.py, at most `jobs` at a time.
type Generator struct {
	python, script, credit string
	slots                  chan struct{}
}

type Result struct {
	PDF   []byte
	PNGs  [][]byte // one per page
	Model string   // steve or alex, as actually used
}

func NewGenerator(python, script, credit string, jobs int) *Generator {
	return &Generator{python: python, script: script, credit: credit, slots: make(chan struct{}, max(jobs, 1))}
}

func (g *Generator) Run(ctx context.Context, skin []byte, model, layers string) (*Result, error) {
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	dir, err := os.MkdirTemp("", "skin-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	in, out := filepath.Join(dir, "skin.png"), filepath.Join(dir, "out")
	if err := os.WriteFile(in, skin, 0o600); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, g.python, g.script, in,
		"--model", model, "--layers", layers, "--credit", g.credit, "--out-dir", out)
	log, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("generator: %w: %s", err, log)
	}

	res := &Result{Model: "steve"}
	if bytes.Contains(log, []byte(": alex model")) {
		res.Model = "alex"
	}
	if res.PDF, err = os.ReadFile(filepath.Join(out, "skin_papercraft.pdf")); err != nil {
		return nil, err
	}
	for page := 1; ; page++ {
		name := "skin_papercraft.png"
		if page > 1 {
			name = fmt.Sprintf("skin_papercraft_page%d.png", page)
		}
		data, err := os.ReadFile(filepath.Join(out, name))
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return nil, err
		}
		res.PNGs = append(res.PNGs, data)
	}
	if len(res.PNGs) == 0 {
		return nil, errors.New("generator produced no pages")
	}
	return res, nil
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
