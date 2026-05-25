package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"walletra/internal/bot"
	"walletra/internal/config"
	"walletra/internal/db"
	"walletra/internal/logger"
	"walletra/internal/repo"
	"walletra/internal/scheduler"
	"walletra/internal/service"
	"walletra/internal/tracker"
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
	userCommands := []tgbotapi.BotCommand{
		{Command: "start", Description: "Start bot"},
		{Command: "help", Description: "Help center"},
		{Command: "profile", Description: "Show your profile"},
	}
	_, _ = telegramBot.Request(tgbotapi.NewSetMyCommandsWithScope(
		tgbotapi.BotCommandScope{Type: "default"},
		userCommands...,
	))

	if cfg.SuperUserTelegram > 0 {
		superCommands := append([]tgbotapi.BotCommand{}, userCommands...)
		superCommands = append(superCommands,
			tgbotapi.BotCommand{Command: "admin", Description: "Superuser help center"},
			tgbotapi.BotCommand{Command: "admin_help", Description: "Superuser help center"},
			tgbotapi.BotCommand{Command: "admin_users", Description: "List users (superuser)"},
			tgbotapi.BotCommand{Command: "admin_activity", Description: "User activities (superuser)"},
		)
		_, _ = telegramBot.Request(tgbotapi.NewSetMyCommandsWithScope(
			tgbotapi.BotCommandScope{Type: "chat", ChatID: cfg.SuperUserTelegram},
			superCommands...,
		))
	}

	r := repo.New(dbPool)
	if cfg.SuperUserTelegram > 0 {
		updated, err := r.EnsureSuperUserByTelegramID(ctx, cfg.SuperUserTelegram)
		if err != nil {
			l.Printf("superuser bootstrap failed telegram_id=%d err=%v", cfg.SuperUserTelegram, err)
		} else if updated {
			l.Printf("superuser role ensured telegram_id=%d", cfg.SuperUserTelegram)
		} else {
			l.Printf("superuser telegram_id=%d is not registered yet; role will be applied after first /start", cfg.SuperUserTelegram)
		}
	}
	reportSvc := service.NewReportService(r)
	var provider *tracker.MultiChainProvider
	startupHealthy := true
	if !cfg.PolygonDirectProviderEnabled {
		l.Printf("POLYGON_DIRECT_PROVIDER_ENABLED is false; forcing direct provider mode because explorer providers were removed")
	}
	if cfg.PolygonRPCURL == "" {
		l.Fatalf("POLYGON_RPC_URL is required for Polygon direct provider mode")
	}
	polyDirect, err := tracker.NewPolygonDirectProvider(r, tracker.PolygonDirectConfig{
		RPCURL:        cfg.PolygonRPCURL,
		StartBlock:    cfg.PolygonStartBlock,
		Confirmations: cfg.PolygonConfirmations,
		ChunkSize:     cfg.PolygonScanChunkSize,
	})
	if err != nil {
		l.Fatalf("polygon direct provider init failed: %v", err)
	}
	provider = tracker.NewMultiChainProvider(polyDirect)
	if err := provider.ValidateAll(ctx); err != nil {
		startupHealthy = false
		l.Printf("provider health-check failed (continuing in degraded mode): %v", err)
		if cfg.SuperUserTelegram > 0 {
			msg := tgbotapi.NewMessage(
				cfg.SuperUserTelegram,
				"⚠️ Provider health-check failed at startup.\n\nBot is running in degraded mode.\n\nError:\n"+err.Error(),
			)
			if _, sendErr := telegramBot.Send(msg); sendErr != nil {
				l.Printf("failed to notify superuser about provider failure: %v", sendErr)
			}
		}
	}
	trackerSvc := service.NewTrackerService(r, provider, l, time.Duration(cfg.PollIntervalMinute)*time.Minute)
	handler := bot.NewHandler(l, r, reportSvc, cfg.SuperUserTelegram)
	s := scheduler.New(l, r, trackerSvc, reportSvc, telegramBot)
	s.Start(ctx, time.Duration(cfg.PollIntervalMinute)*time.Minute)
	if startupHealthy && cfg.SuperUserTelegram > 0 {
		msg := tgbotapi.NewMessage(
			cfg.SuperUserTelegram,
			"✅ Walletra started successfully.\n\nAll startup checks passed.",
		)
		if _, err := telegramBot.Send(msg); err != nil {
			l.Printf("failed to notify superuser about successful startup: %v", err)
		}
	}

	go trackerSvc.PollOnce(ctx)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30
	updates := telegramBot.GetUpdatesChan(u)
	updateWorkers := make(chan struct{}, 32)

	for {
		select {
		case <-ctx.Done():
			log.Println("shutdown")
			return
		case update := <-updates:
			updateWorkers <- struct{}{}
			go func(upd tgbotapi.Update) {
				defer func() { <-updateWorkers }()
				handler.HandleUpdate(ctx, telegramBot, upd)
			}(update)
		}
	}
}
