package main

import (
	"context"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mymmrac/telego"
)

// fakeAPI answers uploads like the Bot API, counting the files it got.
type fakeAPI struct {
	mu      sync.Mutex
	calls   []string
	files   int
	limited bool // answer the first call with flood control
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	method := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]
	if !f.limited {
		f.limited = true
		fmt.Fprint(w, `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":1}}`)
		return
	}
	f.calls = append(f.calls, method)
	n := 0
	if _, params, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil {
		mr := multipart.NewReader(r.Body, params["boundary"])
		for p, err := mr.NextPart(); err == nil; p, err = mr.NextPart() {
			if p.FileName() != "" {
				n++
			}
		}
	}
	f.files += n
	doc := func(i int) string {
		f.files++
		return fmt.Sprintf(`{"message_id":%d,"date":0,"chat":{"id":-100,"type":"channel"},"document":{"file_id":"f%d","file_unique_id":"u%d"}}`, i, f.files, f.files)
	}
	switch method {
	case "sendDocument":
		fmt.Fprintf(w, `{"ok":true,"result":%s}`, doc(1))
	case "sendMediaGroup":
		var msgs []string
		for i := range n {
			msgs = append(msgs, doc(i))
		}
		fmt.Fprintf(w, `{"ok":true,"result":[%s]}`, strings.Join(msgs, ","))
	case "sendPhoto":
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":-100,"type":"channel"},"photo":[{"file_id":"small","file_unique_id":"s","width":90,"height":90},{"file_id":"big","file_unique_id":"b","width":900,"height":900}]}}`)
	default:
		http.Error(w, method, http.StatusNotFound)
	}
}

func TestArchive(t *testing.T) {
	model := testModel(t)
	api := &fakeAPI{}
	srv := httptest.NewServer(api)
	defer srv.Close()
	bot, err := telego.NewBot("123456:"+strings.Repeat("A", 35), telego.WithAPIServer(srv.URL), telego.WithDiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := &App{bot: bot, store: store, gen: NewGenerator("x", 1), archiver: archiver{chat: -100}}
	c := &Craft{UserID: 1, Kind: kindModel, Title: "m", Skin: model}
	if err := store.AddCraft(ctx, c); err != nil {
		t.Fatal(err)
	}
	// The user got the PDF already; only PNGs and the preview are left to upload.
	if err := store.SetFileIDs(ctx, c.ID, "pdf", "user-pdf"); err != nil {
		t.Fatal(err)
	}
	a.archiveStored(ctx)

	got, _ := store.Craft(ctx, c.ID)
	if len(got.Skin) != 0 {
		t.Fatal("model kept")
	}
	pages := len(strings.Split(got.PNGFileIDs, ","))
	if got.PDFFileID != "user-pdf" || got.PreviewFileID != "big" || pages < 2 {
		t.Fatalf("file ids: %+v", got)
	}
	t.Logf("%d pages, calls %v", pages, api.calls)
	for _, m := range api.calls {
		if m == "sendDocument" && pages%10 != 1 {
			t.Fatalf("pdf uploaded again or stray document: %v", api.calls)
		}
	}
}
