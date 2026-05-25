package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	Env                string
	Port               string
	TelegramBotToken   string
	SuperUserTelegram  int64
	DatabaseURL        string
	PollIntervalMinute int
	ExplorerAPIKey     string
}

func Load() (Config, error) {
	// Best-effort local env loading. In Docker/production, process env still wins.
	_ = godotenv.Load(".env")

	superUser, err := strconv.ParseInt(getEnv("SUPERUSER_TELEGRAM_ID", "0"), 10, 64)
	if err != nil {
		return Config{}, fmt.Errorf("parse SUPERUSER_TELEGRAM_ID: %w", err)
	}
	pollMins, err := strconv.Atoi(getEnv("POLL_INTERVAL_MINUTES", "30"))
	if err != nil {
		return Config{}, fmt.Errorf("parse POLL_INTERVAL_MINUTES: %w", err)
	}

	cfg := Config{
		Env:                getEnv("APP_ENV", "dev"),
		Port:               getEnv("PORT", "8080"),
		TelegramBotToken:   os.Getenv("TELEGRAM_BOT_TOKEN"),
		SuperUserTelegram:  superUser,
		DatabaseURL:        os.Getenv("DATABASE_URL"),
		PollIntervalMinute: pollMins,
		ExplorerAPIKey:     os.Getenv("EXPLORER_API_KEY"),
	}

	if cfg.TelegramBotToken == "" {
		return Config{}, fmt.Errorf("TELEGRAM_BOT_TOKEN is required")
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	return cfg, nil
}

func getEnv(k, fallback string) string {
	v := os.Getenv(k)
	if v == "" {
		return fallback
	}
	return v
}
