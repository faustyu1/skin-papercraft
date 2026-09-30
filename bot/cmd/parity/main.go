// Command parity renders a skin or model with the Go generator into a folder, to compare
// against the Python reference output.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"skinbot/papercraft"
)

func main() {
	kind := flag.String("kind", "skin", "skin or model")
	credit := flag.String("credit", "tg: @faustyu", "")
	pixelMM := flag.Float64("pixel-mm", 2.5, "")
	unitMM := flag.Float64("unit-mm", 8, "")
	dpi := flag.Int("dpi", 300, "")
	layers := flag.String("layers", "separate", "")
	model := flag.String("model", "auto", "")
	outDir := flag.String("out-dir", "out", "")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: parity [flags] input")
		os.Exit(2)
	}
	in := flag.Arg(0)
	base := strings.TrimSuffix(filepath.Base(in), filepath.Ext(in))
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		panic(err)
	}
	data, err := os.ReadFile(in)
	if err != nil {
		panic(err)
	}

	level := 6
	if *kind != "skin" {
		level = 3
	}
	pdf := papercraft.NewPdfWriter(*dpi)
	page := 0
	emit := func(im *papercraft.Img) error {
		name := base + "_papercraft.png"
		if page > 0 {
			name = fmt.Sprintf("%s_papercraft_page%d.png", base, page+1)
		}
		page++
		pngData, err := papercraft.SavePNG(im, *dpi, level)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(*outDir, name), pngData, 0o644); err != nil {
			return err
		}
		pdf.Add(im)
		return nil
	}

	if *kind == "skin" {
		skin, err := papercraft.LoadSkin(data)
		if err != nil {
			panic(err)
		}
		var forceSlim *bool
		switch *model {
		case "steve":
			v := false
			forceSlim = &v
		case "alex":
			v := true
			forceSlim = &v
		}
		slim, log, err := papercraft.RenderSkin(skin, *pixelMM, *dpi, *layers, forceSlim, *credit, emit)
		if err != nil {
			panic(err)
		}
		fmt.Print(log)
		name := "steve"
		if slim {
			name = "alex"
		}
		fmt.Printf("%s: %s model, %d page(s)\n", filepath.Base(in), name, page)
	} else {
		unit, log, err := papercraft.RenderModel(data, *unitMM, *dpi, *credit, emit)
		if err != nil {
			panic(err)
		}
		fmt.Print(log)
		fmt.Printf("%s: unit %g mm, %d page(s)\n", filepath.Base(in), unit, page)
	}

	var buf strings.Builder
	if err := pdf.Write(&buf); err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(*outDir, base+"_papercraft.pdf"), []byte(buf.String()), 0o644); err != nil {
		panic(err)
	}
}
