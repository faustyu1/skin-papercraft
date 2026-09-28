package main

import (
	"fmt"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

// Callback data is "<action>[:<arg>...]", see App.onCallback.

func button(text, data string) telego.InlineKeyboardButton {
	return tu.InlineKeyboardButton(text).WithCallbackData(data)
}

func menuRow() []telego.InlineKeyboardButton {
	return tu.InlineKeyboardRow(button(btnMenu, "menu"))
}

func menuKeyboard() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(button(btnNick, "nick")),
		tu.InlineKeyboardRow(button(btnMy, listMy+":0"), button(btnGallery, listGallery+":0")),
		tu.InlineKeyboardRow(button(btnHelp, "help")),
	)
}

func backKeyboard() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(menuRow())
}

func nickRetryKeyboard() *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(tu.InlineKeyboardRow(button(btnRetry, "nick")), menuRow())
}

func choiceRow(field string, options [][2]string, current string) []telego.InlineKeyboardButton {
	row := make([]telego.InlineKeyboardButton, 0, len(options))
	for _, opt := range options {
		label := opt[1]
		if opt[0] == current {
			label = "• " + label
		}
		row = append(row, button(label, "set:"+field+":"+opt[0]))
	}
	return row
}

func draftKeyboard(d *Draft) *telego.InlineKeyboardMarkup {
	if d.Kind == kindModel {
		return tu.InlineKeyboard(
			choiceRow("format", formatButtons, d.Format),
			tu.InlineKeyboardRow(button(btnGenerate, "gen")),
			tu.InlineKeyboardRow(button(btnCancel, "cancel")),
		)
	}
	return tu.InlineKeyboard(
		choiceRow("model", modelButtons, d.Model),
		choiceRow("layers", layerButtons, d.Layers),
		choiceRow("format", formatButtons, d.Format),
		tu.InlineKeyboardRow(button(btnGenerate, "gen")),
		tu.InlineKeyboardRow(button(btnCancel, "cancel")),
	)
}

func publishButton(c *Craft, list string, offset int) telego.InlineKeyboardButton {
	label := btnPublish
	if c.Public {
		label = btnUnpublish
	}
	return button(label, fmt.Sprintf("pub:%d:%s:%d", c.ID, list, offset))
}

// resultKeyboard goes under the file sent right after generation.
func resultKeyboard(c *Craft) *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(publishButton(c, "res", 0)),
		tu.InlineKeyboardRow(button(btnMy, listMy+":0"), button(btnMenu, "menu")),
	)
}

func cardKeyboard(list string, c *Craft, offset, total int, admin bool) *telego.InlineKeyboardMarkup {
	var rows [][]telego.InlineKeyboardButton
	if total > 1 {
		rows = append(rows, tu.InlineKeyboardRow(
			button("‹", fmt.Sprintf("%s:%d", list, (offset-1+total)%total)),
			button(fmt.Sprintf("%d / %d", offset+1, total), "noop"),
			button("›", fmt.Sprintf("%s:%d", list, (offset+1)%total)),
		))
	}
	rows = append(rows, tu.InlineKeyboardRow(
		button(btnPDF, fmt.Sprintf("dl:%d:pdf", c.ID)),
		button(btnPNG, fmt.Sprintf("dl:%d:png", c.ID)),
	))
	switch {
	case list == listMy:
		rows = append(rows,
			tu.InlineKeyboardRow(publishButton(c, list, offset)),
			tu.InlineKeyboardRow(button(btnDelete, fmt.Sprintf("delask:%d:%d", c.ID, offset))),
		)
	case admin:
		rows = append(rows, tu.InlineKeyboardRow(publishButton(c, list, offset)))
	}
	return tu.InlineKeyboard(append(rows, menuRow())...)
}

func deleteKeyboard(id int64, offset int) *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(tu.InlineKeyboardRow(
		button(btnDeleteYes, fmt.Sprintf("del:%d:%d", id, offset)),
		button(btnDeleteNo, fmt.Sprintf("%s:%d", listMy, offset)),
	))
}
