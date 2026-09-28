package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
)

const stateNick = "nick"

type App struct {
	bot    *telego.Bot
	store  *Store
	gen    *Generator
	admins map[int64]bool
	busy   sync.Map // user id -> generation in progress
}

// ---------------------------------------------------------------- messages

func (a *App) onStart(ctx *th.Context, msg telego.Message) error {
	if msg.From == nil || msg.Chat.Type != telego.ChatTypePrivate {
		return nil
	}
	if _, err := a.store.User(ctx, msg.From.ID); err != nil {
		return err
	}
	if err := a.store.SetState(ctx, msg.From.ID, ""); err != nil {
		return err
	}
	return a.send(ctx, msg.Chat.ID, textMenu, menuKeyboard())
}

func (a *App) onMessage(ctx *th.Context, msg telego.Message) error {
	if msg.From == nil || msg.Chat.Type != telego.ChatTypePrivate {
		return nil
	}
	user, err := a.store.User(ctx, msg.From.ID)
	if err != nil {
		return err
	}
	switch {
	case msg.Document != nil:
		return a.onDocument(ctx, user, msg)
	case len(msg.Photo) > 0:
		return a.send(ctx, msg.Chat.ID, textPhotoHint, backKeyboard())
	case msg.Text != "" && user.State == stateNick:
		return a.onNick(ctx, user, msg)
	default:
		return a.send(ctx, msg.Chat.ID, textUnknown, menuKeyboard())
	}
}

func (a *App) onDocument(ctx context.Context, user *User, msg telego.Message) error {
	doc := msg.Document
	if strings.EqualFold(filepath.Ext(doc.FileName), ".bbmodel") {
		return a.onModel(ctx, user, msg)
	}
	if !strings.EqualFold(filepath.Ext(doc.FileName), ".png") && doc.MimeType != "image/png" {
		return a.send(ctx, msg.Chat.ID, textNotPNG, backKeyboard())
	}
	if int64(doc.FileSize) > maxSkinDownload {
		return a.send(ctx, msg.Chat.ID, textTooBig, backKeyboard())
	}
	data, err := a.download(ctx, doc.FileID)
	if err != nil {
		return a.send(ctx, msg.Chat.ID, textDownloadErr, backKeyboard())
	}
	if !validSkin(data) {
		return a.send(ctx, msg.Chat.ID, textBadSkin, backKeyboard())
	}
	title := strings.TrimSuffix(doc.FileName, filepath.Ext(doc.FileName))
	return a.startDraft(ctx, user, msg.Chat.ID, title, data, "auto")
}

var modelErrors = map[error]string{
	errNotModel:      textModelNotModel,
	errNoCubes:       textModelNoCubes,
	errMesh:          textModelMesh,
	errNoTexture:     textModelNoTexture,
	errLinkedTexture: textModelLinked,
}

// onModel takes a Blockbench .bbmodel file sent as a document.
func (a *App) onModel(ctx context.Context, user *User, msg telego.Message) error {
	doc := msg.Document
	if int64(doc.FileSize) > maxModelDownload {
		return a.send(ctx, msg.Chat.ID, textModelTooBig, backKeyboard())
	}
	data, err := a.download(ctx, doc.FileID)
	if err != nil {
		return a.send(ctx, msg.Chat.ID, textDownloadErr, backKeyboard())
	}
	cubes, err := checkModel(data)
	if err != nil {
		return a.send(ctx, msg.Chat.ID, modelErrors[err], backKeyboard())
	}
	title := cleanTitle(strings.TrimSuffix(doc.FileName, filepath.Ext(doc.FileName)))
	d := &Draft{UserID: user.ID, Kind: kindModel, Title: title, Skin: data, Layers: user.Layers, Format: user.Format}
	if err := a.store.SaveDraft(ctx, d); err != nil {
		return err
	}
	if err := a.store.SetState(ctx, user.ID, ""); err != nil {
		return err
	}
	return a.send(ctx, msg.Chat.ID, fmt.Sprintf(textModelDraft, title, cubes), draftKeyboard(d))
}

func (a *App) download(ctx context.Context, fileID string) ([]byte, error) {
	file, err := a.bot.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		log.Printf("get file: %v", err)
		return nil, err
	}
	data, err := tu.DownloadFile(a.bot.FileDownloadURL(file.FilePath))
	if err != nil {
		log.Printf("download file: %v", err)
	}
	return data, err
}

func (a *App) onNick(ctx context.Context, user *User, msg telego.Message) error {
	nick := strings.TrimSpace(msg.Text)
	if !nickPattern.MatchString(nick) {
		return a.send(ctx, msg.Chat.ID, textNickBad, backKeyboard())
	}
	status, err := a.sendMessage(ctx, msg.Chat.ID, textNickLookup, nil)
	if err != nil {
		return err
	}
	skin, name, slim, err := FetchSkin(ctx, nick)
	if err == nil && !validSkin(skin) {
		err = errNoSkin
	}
	if err != nil {
		if !errors.Is(err, errNoSkin) {
			log.Printf("fetch skin %q: %v", nick, err)
		}
		return a.edit(ctx, msg.Chat.ID, status.MessageID, fmt.Sprintf(textNickFailed, nick), nickRetryKeyboard())
	}
	a.deleteMessage(ctx, msg.Chat.ID, status.MessageID)

	model := "steve"
	if slim {
		model = "alex"
	}
	return a.startDraft(ctx, user, msg.Chat.ID, name, skin, model)
}

func (a *App) startDraft(ctx context.Context, user *User, chatID int64, title string, skin []byte, model string) error {
	title = cleanTitle(title)
	d := &Draft{UserID: user.ID, Kind: kindSkin, Title: title, Skin: skin, Model: model, Layers: user.Layers, Format: user.Format}
	if err := a.store.SaveDraft(ctx, d); err != nil {
		return err
	}
	if err := a.store.SetState(ctx, user.ID, ""); err != nil {
		return err
	}
	return a.send(ctx, chatID, fmt.Sprintf(textDraft, title), draftKeyboard(d))
}

// ---------------------------------------------------------------- buttons

func (a *App) onCallback(ctx *th.Context, q telego.CallbackQuery) error {
	answered := false
	answer := func(text string) {
		if answered {
			return
		}
		answered = true
		if err := a.bot.AnswerCallbackQuery(ctx, tu.CallbackQuery(q.ID).WithText(text)); err != nil {
			log.Printf("answer callback: %v", err)
		}
	}
	defer answer("")

	if q.Message == nil {
		return nil
	}
	chatID := q.Message.GetChat().ID
	msg := q.Message.Message() // nil if the message is too old to touch
	if q.Message.GetChat().Type != telego.ChatTypePrivate {
		return nil
	}
	user, err := a.store.User(ctx, q.From.ID)
	if err != nil {
		return err
	}

	parts := strings.Split(q.Data, ":")
	arg := func(i int) string {
		if i < len(parts) {
			return parts[i]
		}
		return ""
	}
	num := func(i int) int64 { n, _ := strconv.ParseInt(arg(i), 10, 64); return n }

	switch parts[0] {
	case "menu":
		if err := a.store.SetState(ctx, user.ID, ""); err != nil {
			return err
		}
		return a.replace(ctx, chatID, msg, textMenu, menuKeyboard())
	case "nick":
		if err := a.store.SetState(ctx, user.ID, stateNick); err != nil {
			return err
		}
		return a.replace(ctx, chatID, msg, textNickPrompt, backKeyboard())
	case "help":
		return a.replace(ctx, chatID, msg, textHelp, backKeyboard())
	case "cancel":
		if err := a.store.DeleteDraft(ctx, user.ID); err != nil {
			return err
		}
		return a.replace(ctx, chatID, msg, textMenu, menuKeyboard())
	case "set":
		return a.onSet(ctx, user, chatID, msg, arg(1), arg(2), answer)
	case "gen":
		return a.onGenerate(ctx, user, chatID, msg, answer)
	case listMy, listGallery:
		answer("")
		return a.showCard(ctx, user, chatID, msg, parts[0], int(num(1)))
	case "dl":
		return a.onDownload(ctx, user, chatID, num(1), arg(2), answer)
	case "pub":
		return a.onPublish(ctx, user, chatID, msg, num(1), arg(2), int(num(3)), answer)
	case "delask":
		if msg == nil {
			return nil
		}
		answer(textDeleteAsk)
		_, err := a.bot.EditMessageReplyMarkup(ctx,
			tu.EditMessageReplyMarkup(tu.ID(chatID), msg.MessageID, deleteKeyboard(num(1), int(num(2)))))
		return ignoreNotModified(err)
	case "del":
		c, err := a.store.Craft(ctx, num(1))
		if err != nil {
			return err
		}
		if c == nil || c.UserID != user.ID {
			answer(textNotFound)
		} else {
			if err := a.store.DeleteCraft(ctx, c.ID); err != nil {
				return err
			}
			answer(textDeleted)
		}
		return a.showCard(ctx, user, chatID, msg, listMy, int(num(2)))
	}
	return nil
}

var draftOptions = map[string][][2]string{"model": modelButtons, "layers": layerButtons, "format": formatButtons}

func (a *App) onSet(ctx context.Context, user *User, chatID int64, msg *telego.Message,
	field, value string, answer func(string)) error {
	valid := false
	for _, opt := range draftOptions[field] {
		valid = valid || opt[0] == value
	}
	if !valid {
		return nil
	}
	d, err := a.store.Draft(ctx, user.ID)
	if err != nil {
		return err
	}
	if d == nil {
		answer(textNoDraft)
		return nil
	}
	if d.Kind == kindModel && field != "format" {
		return nil
	}
	switch field {
	case "model":
		d.Model = value
	case "layers":
		d.Layers = value
	case "format":
		d.Format = value
	}
	if err := a.store.SaveDraft(ctx, d); err != nil {
		return err
	}
	if field != "model" {
		if err := a.store.SetPref(ctx, user.ID, field, value); err != nil {
			return err
		}
	}
	return a.replace(ctx, chatID, msg, draftText(d), draftKeyboard(d))
}

func draftText(d *Draft) string {
	if d.Kind == kindModel {
		cubes, _ := checkModel(d.Skin)
		return fmt.Sprintf(textModelDraft, d.Title, cubes)
	}
	return fmt.Sprintf(textDraft, d.Title)
}

// caption describes a craft under its files and on its card.
func caption(c *Craft) string {
	if c.Kind == kindModel {
		return fmt.Sprintf(textModelInfo, c.Title)
	}
	return fmt.Sprintf(textSkinInfo, c.Title, modelNames[c.Model], layerNames[c.Layers])
}

func (a *App) onGenerate(ctx context.Context, user *User, chatID int64, msg *telego.Message, answer func(string)) error {
	d, err := a.store.Draft(ctx, user.ID)
	if err != nil {
		return err
	}
	if d == nil {
		answer(textNoDraft)
		return nil
	}
	if _, running := a.busy.LoadOrStore(user.ID, true); running {
		answer(textBusy)
		return nil
	}
	defer a.busy.Delete(user.ID)
	answer("")

	status := msg
	if status == nil || status.Text == "" {
		if status, err = a.sendMessage(ctx, chatID, textGenerating, nil); err != nil {
			return err
		}
	} else if err := a.edit(ctx, chatID, status.MessageID, textGenerating, nil); err != nil {
		return err
	}

	res, err := a.gen.Run(ctx, d.Kind, d.Skin, d.Model, d.Layers)
	if err != nil {
		log.Printf("generate for %d: %v", user.ID, err)
		failed := textGenFailed
		switch {
		case d.Kind == kindModel && errors.Is(err, errTooSlow):
			failed = textModelSlow
		case d.Kind == kindModel:
			failed = textModelFailed
		}
		return a.edit(ctx, chatID, status.MessageID, failed, backKeyboard())
	}
	c := &Craft{UserID: user.ID, Kind: d.Kind, Title: d.Title, Skin: d.Skin, Model: res.Model, Layers: d.Layers}
	if err := a.store.AddCraft(ctx, c); err != nil {
		return err
	}
	if err := a.store.DeleteDraft(ctx, user.ID); err != nil {
		return err
	}

	text := caption(c) + textPrint
	if res.Detailed {
		text += textModelDetailed
	}
	if err := a.sendFiles(ctx, chatID, c, d.Format, text, resultKeyboard(c), res); err != nil {
		log.Printf("send files for %d: %v", user.ID, err)
		return a.edit(ctx, chatID, status.MessageID, textGenFailed, backKeyboard())
	}
	a.deleteMessage(ctx, chatID, status.MessageID)
	return nil
}

func (a *App) onDownload(ctx context.Context, user *User, chatID, id int64, format string, answer func(string)) error {
	if format != "pdf" && format != "png" {
		return nil
	}
	c, err := a.store.Craft(ctx, id)
	if err != nil {
		return err
	}
	if c == nil || !(c.UserID == user.ID || c.Public || a.admins[user.ID]) {
		answer(textNotFound)
		return nil
	}
	answer(textSending)
	if err := a.sendFiles(ctx, chatID, c, format, "«"+c.Title+"»", nil, nil); err != nil {
		log.Printf("download %d: %v", id, err)
		return a.send(ctx, chatID, textGenFailed, backKeyboard())
	}
	return nil
}

func (a *App) onPublish(ctx context.Context, user *User, chatID int64, msg *telego.Message,
	id int64, list string, offset int, answer func(string)) error {
	c, err := a.store.Craft(ctx, id)
	if err != nil {
		return err
	}
	owner := c != nil && c.UserID == user.ID
	// Owners toggle freely; admins may only take things down from the gallery.
	if c == nil || !(owner || (a.admins[user.ID] && c.Public)) {
		answer(textNotFound)
		return nil
	}
	c.Public = !c.Public
	if err := a.store.SetPublic(ctx, c.ID, c.Public); err != nil {
		return err
	}
	if c.Public {
		answer(textPublished)
	} else {
		answer(textUnpublished)
	}

	if list == "res" {
		if msg == nil {
			return nil
		}
		_, err := a.bot.EditMessageReplyMarkup(ctx,
			tu.EditMessageReplyMarkup(tu.ID(chatID), msg.MessageID, resultKeyboard(c)))
		return ignoreNotModified(err)
	}
	return a.showCard(ctx, user, chatID, msg, list, offset)
}

// ---------------------------------------------------------------- cards

func (a *App) showCard(ctx context.Context, user *User, chatID int64, msg *telego.Message, list string, offset int) error {
	c, total, offset, err := a.store.CraftAt(ctx, list, user.ID, offset)
	if err != nil {
		return err
	}
	if c == nil {
		empty := textMyEmpty
		if list == listGallery {
			empty = textGalleryEmpty
		}
		return a.replace(ctx, chatID, msg, empty, backKeyboard())
	}

	text := caption(c) + fmt.Sprintf(textCardDate, c.CreatedAt.Format("02.01.2006"))
	if list == listMy {
		text += fmt.Sprintf(textCardPublic, yesNo[c.Public])
	}
	kb := cardKeyboard(list, c, offset, total, a.admins[user.ID])

	photo, uploaded := tu.FileFromID(c.PreviewFileID), false
	if c.PreviewFileID == "" {
		res, err := a.gen.Run(ctx, c.Kind, c.Skin, c.Model, c.Layers)
		if err != nil {
			return err
		}
		photo, uploaded = tu.File(tu.NameReader(bytes.NewReader(res.PNGs[0]), "preview.png")), true
	}

	var sent *telego.Message
	if msg != nil && len(msg.Photo) > 0 {
		sent, err = a.bot.EditMessageMedia(ctx, tu.EditMessageMedia(tu.ID(chatID), msg.MessageID,
			tu.MediaPhoto(photo).WithCaption(text)).WithReplyMarkup(kb))
		if isNotModified(err) {
			return nil
		}
	} else {
		if msg != nil && msg.Text != "" {
			a.deleteMessage(ctx, chatID, msg.MessageID)
		}
		sent, err = a.bot.SendPhoto(ctx, tu.Photo(tu.ID(chatID), photo).WithCaption(text).WithReplyMarkup(kb))
	}
	if err != nil {
		return err
	}
	if uploaded && sent != nil && len(sent.Photo) > 0 {
		return a.store.SetFileIDs(ctx, c.ID, "preview", sent.Photo[len(sent.Photo)-1].FileID)
	}
	return nil
}

// sendFiles sends a craft as PDF or PNG pages, reusing Telegram file ids when it can.
// res may hold an already generated result; otherwise the files are generated on demand.
func (a *App) sendFiles(ctx context.Context, chatID int64, c *Craft, format, caption string,
	kb *telego.InlineKeyboardMarkup, res *Result) error {
	cached := c.PDFFileID
	if format == "png" {
		cached = c.PNGFileIDs
	}

	var files []telego.InputFile
	if cached != "" {
		for _, id := range strings.Split(cached, ",") {
			files = append(files, tu.FileFromID(id))
		}
	} else {
		if res == nil {
			var err error
			if res, err = a.gen.Run(ctx, c.Kind, c.Skin, c.Model, c.Layers); err != nil {
				return err
			}
		}
		base := fileBase(c.Title) + "_papercraft"
		if format == "pdf" {
			files = append(files, tu.File(tu.NameReader(bytes.NewReader(res.PDF), base+".pdf")))
		}
		for i, page := range res.PNGs {
			if format != "png" {
				break
			}
			name := base + ".png"
			if i > 0 {
				name = fmt.Sprintf("%s_page%d.png", base, i+1)
			}
			files = append(files, tu.File(tu.NameReader(bytes.NewReader(page), name)))
		}
	}

	ids := make([]string, 0, len(files))
	for i, file := range files {
		p := tu.Document(tu.ID(chatID), file)
		if i == len(files)-1 {
			p = p.WithCaption(caption)
			if kb != nil {
				p = p.WithReplyMarkup(kb)
			}
		}
		sent, err := a.bot.SendDocument(ctx, p)
		if err != nil {
			return err
		}
		if sent.Document != nil {
			ids = append(ids, sent.Document.FileID)
		}
	}
	if cached == "" && len(ids) == len(files) {
		return a.store.SetFileIDs(ctx, c.ID, format, strings.Join(ids, ","))
	}
	return nil
}

// ---------------------------------------------------------------- helpers

func (a *App) sendMessage(ctx context.Context, chatID int64, text string, kb *telego.InlineKeyboardMarkup) (*telego.Message, error) {
	p := tu.Message(tu.ID(chatID), text)
	if kb != nil {
		p = p.WithReplyMarkup(kb)
	}
	return a.bot.SendMessage(ctx, p)
}

func (a *App) send(ctx context.Context, chatID int64, text string, kb *telego.InlineKeyboardMarkup) error {
	_, err := a.sendMessage(ctx, chatID, text, kb)
	return err
}

func (a *App) edit(ctx context.Context, chatID int64, msgID int, text string, kb *telego.InlineKeyboardMarkup) error {
	p := tu.EditMessageText(tu.ID(chatID), msgID, text)
	if kb != nil {
		p = p.WithReplyMarkup(kb)
	}
	_, err := a.bot.EditMessageText(ctx, p)
	return ignoreNotModified(err)
}

// replace shows text in place of msg: text messages are edited, photo cards are
// swapped for a new message, and files (results) are left alone.
func (a *App) replace(ctx context.Context, chatID int64, msg *telego.Message, text string, kb *telego.InlineKeyboardMarkup) error {
	if msg != nil && msg.Text != "" {
		return a.edit(ctx, chatID, msg.MessageID, text, kb)
	}
	if msg != nil && len(msg.Photo) > 0 {
		a.deleteMessage(ctx, chatID, msg.MessageID)
	}
	return a.send(ctx, chatID, text, kb)
}

func (a *App) deleteMessage(ctx context.Context, chatID int64, msgID int) {
	if err := a.bot.DeleteMessage(ctx, tu.Delete(tu.ID(chatID), msgID)); err != nil {
		log.Printf("delete message: %v", err)
	}
}

func isNotModified(err error) bool {
	return err != nil && strings.Contains(err.Error(), "message is not modified")
}

func ignoreNotModified(err error) error {
	if isNotModified(err) {
		return nil
	}
	return err
}

func cleanTitle(title string) string {
	title = strings.TrimSpace(title)
	if r := []rune(title); len(r) > 40 {
		title = string(r[:40])
	}
	if title == "" {
		title = "Скин"
	}
	return title
}

// fileBase makes a title safe for a file name.
func fileBase(title string) string {
	name := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, title)
	if name = strings.Trim(name, "_"); name == "" {
		name = "skin"
	}
	return name
}
