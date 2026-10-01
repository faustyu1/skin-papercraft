package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testSkin(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../skin.png")
	if err != nil {
		t.Skip("../skin.png not available")
	}
	return data
}

func TestStoreListsAndGallery(t *testing.T) {
	ctx := context.Background()
	s, err := OpenStore(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	u, err := s.User(ctx, 1)
	if err != nil || u.Layers != "separate" || u.Format != "pdf" {
		t.Fatalf("user defaults: %+v %v", u, err)
	}
	if err := s.SetPref(ctx, 1, "format", "png"); err != nil {
		t.Fatal(err)
	}
	if u, _ = s.User(ctx, 1); u.Format != "png" {
		t.Fatalf("pref not saved: %+v", u)
	}

	d := &Draft{UserID: 1, Title: "a", Skin: []byte{1}, Model: "auto", Layers: "flat", Format: "png"}
	if err := s.SaveDraft(ctx, d); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Draft(ctx, 1); got == nil || got.Layers != "flat" {
		t.Fatalf("draft: %+v", got)
	}
	if err := s.DeleteDraft(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Draft(ctx, 1); got != nil {
		t.Fatal("draft not deleted")
	}

	var ids []int64
	for i, owner := range []int64{1, 1, 2} {
		c := &Craft{UserID: owner, Title: string(rune('a' + i)), Skin: []byte{1}, Model: "steve", Layers: "separate"}
		if err := s.AddCraft(ctx, c); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, c.ID)
	}

	c, total, off, err := s.CraftAt(ctx, listMy, 1, 99)
	if err != nil || total != 2 || off != 1 || c.ID != ids[0] {
		t.Fatalf("my list clamp: %+v total=%d off=%d err=%v", c, total, off, err)
	}
	if c, _, _, _ = s.CraftAt(ctx, listGallery, 1, 0); c != nil {
		t.Fatal("gallery should be empty")
	}
	s.SetPublic(ctx, ids[2], true)
	s.SetPublic(ctx, ids[0], true)
	c, total, _, _ = s.CraftAt(ctx, listGallery, 1, 0)
	if total != 2 || c.ID != ids[0] {
		t.Fatalf("gallery newest published first: %+v total=%d", c, total)
	}

	if err := s.SetFileIDs(ctx, ids[0], "png", "x,y"); err != nil {
		t.Fatal(err)
	}
	if c, _ = s.Craft(ctx, ids[0]); c.PNGFileIDs != "x,y" || !c.Public {
		t.Fatalf("craft fields: %+v", c)
	}
	if err := s.DeleteCraft(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	if c, _ = s.Craft(ctx, ids[0]); c != nil {
		t.Fatal("craft not deleted")
	}
}

func TestGenerator(t *testing.T) {
	skin := testSkin(t)
	if !validSkin(skin) {
		t.Fatal("test skin rejected")
	}
	if validSkin([]byte("nope")) {
		t.Fatal("garbage accepted")
	}
	g := NewGenerator("tg: @faustyu", 2)
	for _, model := range []string{"auto", "alex"} {
		res, err := g.Run(context.Background(), kindSkin, skin, model, "separate", wantPDF)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(res.PDF, []byte("%PDF")) || len(res.PNGs) != 0 {
			t.Fatalf("bad output for %s", model)
		}
		want := map[string]string{"auto": "steve", "alex": "alex"}[model]
		if res.Model != want {
			t.Fatalf("model %s: got %s", model, res.Model)
		}
	}
	res, err := g.Run(context.Background(), kindSkin, skin, "auto", "separate", wantPNG)
	if err != nil || len(res.PNGs) == 0 || res.PDF != nil {
		t.Fatalf("png run: %v", err)
	}
	pages := len(res.PNGs)
	res, err = g.Run(context.Background(), kindSkin, skin, "auto", "separate", wantPreview)
	if err != nil || len(res.PNGs) != 1 || res.PDF != nil {
		t.Fatalf("preview run: %v", err)
	}
	t.Logf("%d png page(s)", pages)
}

// testModel returns the smallest .bbmodel next to the bot, to keep the test fast.
func testModel(t *testing.T) []byte {
	t.Helper()
	paths, _ := filepath.Glob("../*.bbmodel")
	var best []byte
	for _, p := range paths {
		if data, err := os.ReadFile(p); err == nil && (best == nil || len(data) < len(best)) {
			best = data
		}
	}
	if best == nil {
		t.Skip("no ../*.bbmodel available")
	}
	return best
}

func TestModelGenerator(t *testing.T) {
	model := testModel(t)
	if n, err := checkModel(model); err != nil || n == 0 {
		t.Fatalf("test model rejected: %d %v", n, err)
	}
	g := NewGenerator("tg: @faustyu", 2)
	res, err := g.Run(context.Background(), kindModel, model, "", "", wantPNG)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.PNGs) < 2 {
		t.Fatalf("bad output: %d page(s)", len(res.PNGs))
	}
	res, err = g.Run(context.Background(), kindModel, model, "", "", wantPDF)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(res.PDF, []byte("%PDF")) {
		t.Fatal("bad pdf")
	}
}

func TestCheckModel(t *testing.T) {
	const tex = `"textures": [{"source": "data:image/png;base64,AAAA"}]`
	for in, want := range map[string]error{
		`not json`:           errNotModel,
		`{"elements": [{}]}`: errNotModel,
		`{"meta": {}, "elements": [], ` + tex + `}`:                                          errNoCubes,
		`{"meta": {}, "elements": [{"type": "cube"}, {"type": "mesh"}], ` + tex + `}`:        errMesh,
		`{"meta": {}, "elements": [{}]}`:                                                     errNoTexture,
		`{"meta": {}, "elements": [{}], "textures": [{"source": ""}]}`:                       errLinkedTexture,
		`{"meta": {}, "elements": [{"type": "cube"}, {"type": "locator"}, {}], ` + tex + `}`: nil,
	} {
		if _, err := checkModel([]byte(in)); err != want {
			t.Errorf("checkModel(%s) = %v, want %v", in, err, want)
		}
	}
}

func TestStoreKind(t *testing.T) {
	ctx := context.Background()
	s, err := OpenStore(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SaveDraft(ctx, &Draft{UserID: 1, Kind: kindModel, Title: "m", Skin: []byte("{}"), Format: "pdf"}); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.Draft(ctx, 1); d == nil || d.Kind != kindModel {
		t.Fatalf("draft kind: %+v", d)
	}
	c := &Craft{UserID: 1, Title: "s", Skin: []byte{1}, Model: "steve", Layers: "none"}
	if err := s.AddCraft(ctx, c); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Craft(ctx, c.ID); got.Kind != kindSkin {
		t.Fatalf("craft kind defaults to skin: %+v", got)
	}
}

func TestStoreDropSource(t *testing.T) {
	ctx := context.Background()
	s, err := OpenStore(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	skin := &Craft{UserID: 1, Title: "s", Skin: []byte{1}, Model: "steve", Layers: "none"}
	model := &Craft{UserID: 1, Kind: kindModel, Title: "m", Skin: []byte("{}")}
	for _, c := range []*Craft{skin, model} {
		if err := s.AddCraft(ctx, c); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"pdf", "png"} {
			if err := s.SetFileIDs(ctx, c.ID, f, "id"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if ids, _ := s.ModelsWithSource(ctx); len(ids) != 1 || ids[0] != model.ID {
		t.Fatalf("models with source: %v", ids)
	}
	// No preview yet: the model is still needed.
	if err := s.DropSource(ctx, model.ID); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Craft(ctx, model.ID); len(c.Skin) == 0 {
		t.Fatal("dropped a model that still has outputs to upload")
	}
	for _, c := range []*Craft{skin, model} {
		if err := s.SetFileIDs(ctx, c.ID, "preview", "id"); err != nil {
			t.Fatal(err)
		}
		if err := s.DropSource(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
	}
	if c, _ := s.Craft(ctx, model.ID); len(c.Skin) != 0 {
		t.Fatal("model kept after upload")
	}
	if c, _ := s.Craft(ctx, skin.ID); len(c.Skin) == 0 {
		t.Fatal("skins are kept")
	}
	if ids, _ := s.ModelsWithSource(ctx); len(ids) != 0 {
		t.Fatalf("models with source after drop: %v", ids)
	}
	if err := s.Vacuum(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := NewGenerator("x", 1).Run(ctx, kindModel, nil, "", "", wantPDF); !errors.Is(err, errNoSource) {
		t.Fatalf("render without source: %v", err)
	}
}

func TestDeleteDraftsBefore(t *testing.T) {
	ctx := context.Background()
	s, err := OpenStore(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, id := range []int64{1, 2} {
		if err := s.SaveDraft(ctx, &Draft{UserID: id, Kind: kindModel, Title: "m", Skin: []byte("{}"), Format: "pdf"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE drafts SET updated_at = ? WHERE user_id = 1`,
		time.Now().Add(-25*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if n, err := s.DeleteDraftsBefore(ctx, time.Now().Add(-draftTTL)); err != nil || n != 1 {
		t.Fatalf("deleted %d: %v", n, err)
	}
	if d, _ := s.Draft(ctx, 1); d != nil {
		t.Fatal("stale draft kept")
	}
	if d, _ := s.Draft(ctx, 2); d == nil {
		t.Fatal("fresh draft dropped")
	}
}

func TestFileBase(t *testing.T) {
	for in, want := range map[string]string{"Стив 2": "Стив_2", "a/b\\c": "a_b_c", "***": "skin", "Notch": "Notch"} {
		if got := fileBase(in); got != want {
			t.Errorf("fileBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFetchSkin(t *testing.T) {
	if os.Getenv("NET_TESTS") == "" {
		t.Skip("set NET_TESTS=1 to query Mojang")
	}
	skin, name, _, err := FetchSkin(context.Background(), "Notch")
	if err != nil || name != "Notch" || !validSkin(skin) {
		t.Fatalf("fetch: name=%q err=%v valid=%v", name, err, validSkin(skin))
	}
	if _, _, _, err := FetchSkin(context.Background(), "zz_no_such_user_zz"); err != errNoSkin {
		t.Fatalf("missing user: %v", err)
	}
}

func TestStoreMigratesOldDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema); err != nil { // the schema before the kind columns

		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO crafts (user_id, title, skin, model, layers, created_at) VALUES (1, 'old', x'01', 'steve', 'none', 0)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for range 2 { // the second start must not trip over the added columns
		s, err := OpenStore(path)
		if err != nil {
			t.Fatal(err)
		}
		c, _ := s.Craft(context.Background(), 1)
		s.Close()
		if c == nil || c.Kind != kindSkin {
			t.Fatalf("old craft: %+v", c)
		}
	}
}
