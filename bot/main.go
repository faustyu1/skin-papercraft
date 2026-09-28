// Telegram bot that turns Minecraft skins into printable papercraft nets.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func main() {
	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("BOT_TOKEN is not set")
	}
	jobs, _ := strconv.Atoi(env("MAX_JOBS", "2"))
	admins := map[int64]bool{}
	for _, id := range strings.Split(os.Getenv("ADMIN_IDS"), ",") {
		if n, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64); err == nil {
			admins[n] = true
		}
	}

	store, err := OpenStore(env("DB_PATH", "bot.db"))
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	bot, err := telego.NewBot(token, telego.WithDefaultLogger(false, true))
	if err != nil {
		log.Fatalf("create bot: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err = bot.SetMyCommands(ctx, &telego.SetMyCommandsParams{
		Commands: []telego.BotCommand{{Command: "start", Description: "Главное меню"}},
	})
	if err != nil {
		log.Printf("set commands: %v", err)
	}

	updates, err := bot.UpdatesViaLongPolling(ctx, nil)
	if err != nil {
		log.Fatalf("long polling: %v", err)
	}
	bh, err := th.NewBotHandler(bot, updates, th.WithErrorHandler(func(_ *th.Context, u telego.Update, err error) {
		log.Printf("update %d: %v", u.UpdateID, err)
	}))
	if err != nil {
		log.Fatalf("bot handler: %v", err)
	}

	app := &App{
		bot:   bot,
		store: store,
		gen: NewGenerator(env("PYTHON", "python3"), env("GENERATOR", "../skin_papercraft.py"),
			env("MODEL_GENERATOR", "../bbmodel_papercraft.py"), env("CREDIT", "tg: @faustyu"), jobs),
		admins: admins,
	}
	bh.HandleMessage(app.onStart, th.CommandEqual("start"))
	bh.HandleMessage(app.onMessage)
	bh.HandleCallbackQuery(app.onCallback)

	go func() {
		<-ctx.Done()
		_ = bh.Stop()
	}()
	log.Println("bot started")
	if err := bh.Start(); err != nil {
		log.Printf("bot handler stopped: %v", err)
	}
}
