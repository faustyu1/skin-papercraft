package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	tu "github.com/mymmrac/telego/telegoutil"
)

// Blockbench models run to megabytes, so they are not kept: once a model craft is made,
// its PDF, PNG pages and card preview go to the storage chat, and from then on the craft
// is sent from those Telegram file ids alone.

// archiver uploads model crafts to the storage chat one at a time.
type archiver struct {
	chat int64 // 0 keeps .bbmodel files in the database
	mu   sync.Mutex
}

// archive uploads whatever outputs of a model craft have no file id yet, then drops the
// .bbmodel. res may hold the craft rendered with wantAll; otherwise it is rendered again.
func (a *App) archive(ctx context.Context, id int64, res *Result) error {
	a.archiver.mu.Lock()
	defer a.archiver.mu.Unlock()
	c, err := a.store.Craft(ctx, id)
	if err != nil || c == nil || c.Kind != kindModel || len(c.Skin) == 0 {
		return err
	}
	if res == nil && (c.PDFFileID == "" || c.PNGFileIDs == "" || c.PreviewFileID == "") {
		if res, err = a.gen.Run(ctx, c.Kind, c.Skin, c.Model, c.Layers, wantAll); err != nil {
			return fmt.Errorf("render: %w", err)
		}
	}
	chat := tu.ID(a.archiver.chat)
	base := fileBase(c.Title) + "_papercraft"
	caption := fmt.Sprintf("#%d «%s»", c.ID, c.Title)

	if c.PDFFileID == "" {
		sent, err := retry(ctx, func() (*telego.Message, error) {
			file := tu.File(tu.NameReader(bytes.NewReader(res.PDF), base+".pdf"))
			return a.bot.SendDocument(ctx, tu.Document(chat, file).WithCaption(caption))
		})
		if err != nil {
			return fmt.Errorf("upload pdf: %w", err)
		}
		if err := a.store.SetFileIDs(ctx, c.ID, "pdf", sent.Document.FileID); err != nil {
			return err
		}
	}

	if c.PNGFileIDs == "" {
		var ids []string
		for start := 0; start < len(res.PNGs); start += 10 {
			pages := res.PNGs[start:min(start+10, len(res.PNGs))]
			sent, err := retry(ctx, func() ([]telego.Message, error) {
				if len(pages) == 1 { // a media group needs at least two files
					file := tu.File(tu.NameReader(bytes.NewReader(pages[0]), pageName(base, start)))
					m, err := a.bot.SendDocument(ctx, tu.Document(chat, file).WithCaption(caption))
					if err != nil {
						return nil, err
					}
					return []telego.Message{*m}, nil
				}
				var media []telego.InputMedia
				for i, page := range pages {
					doc := tu.MediaDocument(tu.File(tu.NameReader(bytes.NewReader(page), pageName(base, start+i))))
					if i == len(pages)-1 {
						doc = doc.WithCaption(caption)
					}
					media = append(media, doc)
				}
				return a.bot.SendMediaGroup(ctx, tu.MediaGroup(chat, media...))
			})
			if err != nil {
				return fmt.Errorf("upload png: %w", err)
			}
			for _, m := range sent {
				if m.Document == nil {
					return errors.New("upload png: no document in reply")
				}
				ids = append(ids, m.Document.FileID)
			}
		}
		if err := a.store.SetFileIDs(ctx, c.ID, "png", strings.Join(ids, ",")); err != nil {
			return err
		}
	}

	if c.PreviewFileID == "" {
		sent, err := retry(ctx, func() (*telego.Message, error) {
			photo := tu.File(tu.NameReader(bytes.NewReader(res.PNGs[0]), "preview.png"))
			return a.bot.SendPhoto(ctx, tu.Photo(chat, photo).WithCaption(caption))
		})
		if err != nil {
			return fmt.Errorf("upload preview: %w", err)
		}
		if len(sent.Photo) == 0 {
			return errors.New("upload preview: no photo in reply")
		}
		if err := a.store.SetFileIDs(ctx, c.ID, "preview", sent.Photo[len(sent.Photo)-1].FileID); err != nil {
			return err
		}
	}
	return a.store.DropSource(ctx, c.ID)
}

// archiveLater archives a fresh craft in the background, after the user has their files.
func (a *App) archiveLater(id int64, res *Result) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := a.archive(ctx, id, res); err != nil {
			log.Printf("archive craft %d: %v", id, err)
		}
	}()
}

// archiveStored moves the models crafted before the storage chat was set out of the
// database, one by one, then compacts the database file.
func (a *App) archiveStored(ctx context.Context) {
	ids, err := a.store.ModelsWithSource(ctx)
	if err != nil {
		log.Printf("archive stored models: %v", err)
		return
	}
	if len(ids) == 0 {
		return
	}
	log.Printf("archiving %d stored model(s)", len(ids))
	done := 0
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		if err := a.archive(ctx, id, nil); err != nil {
			log.Printf("archive craft %d: %v", id, err)
			continue
		}
		done++
	}
	log.Printf("archived %d of %d stored model(s)", done, len(ids))
	if done > 0 {
		if err := a.store.Vacuum(ctx); err != nil {
			log.Printf("vacuum: %v", err)
		}
	}
}

// retry repeats a Telegram call that hit flood control, after the wait Telegram asks for.
func retry[T any](ctx context.Context, call func() (T, error)) (T, error) {
	for attempt := 0; ; attempt++ {
		v, err := call()
		var apiErr *ta.Error
		if err == nil || attempt == 4 || !errors.As(err, &apiErr) ||
			apiErr.Parameters == nil || apiErr.Parameters.RetryAfter == 0 {
			return v, err
		}
		select {
		case <-time.After(time.Duration(apiErr.Parameters.RetryAfter+1) * time.Second):
		case <-ctx.Done():
			return v, ctx.Err()
		}
	}
}
