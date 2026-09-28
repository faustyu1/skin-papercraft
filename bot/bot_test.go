package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
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
	g := NewGenerator("python3", "../skin_papercraft.py", "tg: @faustyu", 2)
	for _, model := range []string{"auto", "alex"} {
		res, err := g.Run(context.Background(), skin, model, "separate")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(res.PDF, []byte("%PDF")) || len(res.PNGs) == 0 {
			t.Fatalf("bad output for %s", model)
		}
		want := map[string]string{"auto": "steve", "alex": "alex"}[model]
		if res.Model != want {
			t.Fatalf("model %s: got %s", model, res.Model)
		}
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
