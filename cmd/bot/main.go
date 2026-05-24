package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"wallet_tracker_bot/internal/bot"
	"wallet_tracker_bot/internal/config"
	"wallet_tracker_bot/internal/db"
	"wallet_tracker_bot/internal/logger"
	"wallet_tracker_bot/internal/repo"
	"wallet_tracker_bot/internal/scheduler"
	"wallet_tracker_bot/internal/service"
	"wallet_tracker_bot/internal/tracker"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	l := logger.New()
	cfg, err := config.Load()
	if err != nil {
		l.Fatalf("config error: %v", err)
	}

	dbPool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		l.Fatalf("db error: %v", err)
	}
	defer dbPool.Close()

	telegramBot, err := tgbotapi.NewBotAPI(cfg.TelegramBotToken)
	if err != nil {
		l.Fatalf("telegram bot init error: %v", err)
	}
	telegramBot.Debug = cfg.Env != "prod"
	l.Printf("bot authorized as @%s", telegramBot.Self.UserName)

	r := repo.New(dbPool)
	reportSvc := service.NewReportService(r)
	provider := tracker.NewCovalentProvider(cfg.CovalentAPIKey)
	trackerSvc := service.NewTrackerService(r, provider, l)
	handler := bot.NewHandler(l, r, reportSvc, cfg.SuperUserTelegram)
	s := scheduler.New(l, r, trackerSvc, reportSvc, telegramBot)
	s.Start(ctx, time.Duration(cfg.PollIntervalMinute)*time.Minute)

	trackerSvc.PollOnce(ctx)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30
	updates := telegramBot.GetUpdatesChan(u)

	for {
		select {
		case <-ctx.Done():
			log.Println("shutdown")
			return
		case update := <-updates:
			handler.HandleUpdate(ctx, telegramBot, update)
		}
	}
}
