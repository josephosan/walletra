package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	Env                          string
	Port                         string
	TelegramBotToken             string
	SuperUserTelegram            int64
	DatabaseURL                  string
	PollIntervalMinute           int
	PolygonRPCURL                string
	PolygonWSURL                 string
	PolygonStartBlock            int64
	PolygonConfirmations         int64
	PolygonScanChunkSize         int64
	PolygonDirectProviderEnabled bool
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
	startBlock, err := strconv.ParseInt(getEnv("POLYGON_START_BLOCK", "0"), 10, 64)
	if err != nil {
		return Config{}, fmt.Errorf("parse POLYGON_START_BLOCK: %w", err)
	}
	confirmations, err := strconv.ParseInt(getEnv("POLYGON_CONFIRMATIONS", "20"), 10, 64)
	if err != nil {
		return Config{}, fmt.Errorf("parse POLYGON_CONFIRMATIONS: %w", err)
	}
	chunkSize, err := strconv.ParseInt(getEnv("POLYGON_SCAN_CHUNK_SIZE", "500"), 10, 64)
	if err != nil {
		return Config{}, fmt.Errorf("parse POLYGON_SCAN_CHUNK_SIZE: %w", err)
	}
	directEnabled, err := strconv.ParseBool(getEnv("POLYGON_DIRECT_PROVIDER_ENABLED", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("parse POLYGON_DIRECT_PROVIDER_ENABLED: %w", err)
	}

	cfg := Config{
		Env:                          getEnv("APP_ENV", "dev"),
		Port:                         getEnv("PORT", "8080"),
		TelegramBotToken:             os.Getenv("TELEGRAM_BOT_TOKEN"),
		SuperUserTelegram:            superUser,
		DatabaseURL:                  os.Getenv("DATABASE_URL"),
		PollIntervalMinute:           pollMins,
		PolygonRPCURL:                getEnv("POLYGON_RPC_URL", ""),
		PolygonWSURL:                 getEnv("POLYGON_WS_URL", ""),
		PolygonStartBlock:            startBlock,
		PolygonConfirmations:         confirmations,
		PolygonScanChunkSize:         chunkSize,
		PolygonDirectProviderEnabled: directEnabled,
	}

	if cfg.TelegramBotToken == "" {
		return Config{}, fmt.Errorf("TELEGRAM_BOT_TOKEN is required")
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.PolygonDirectProviderEnabled && cfg.PolygonRPCURL == "" {
		return Config{}, fmt.Errorf("POLYGON_RPC_URL is required when POLYGON_DIRECT_PROVIDER_ENABLED=true")
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
