# Walletra

Telegram bot: <a href="https://t.me/walletra_bot" target="_blank">@walletra_bot</a>

Telegram bot to track wallet transactions and generate hourly/daily/monthly/yearly reports.

## Features

- Role model: `user` and single `superuser` (from env)
- Add wallets with chain + token filters
- Poll wallets every 30 minutes (configurable)
- Scheduled auto reports per user settings
- On-demand reports from Telegram buttons
- Settings:
  - report frequency (`hourly`, `daily`, `monthly`, `yearly`)
  - include unchanged wallets toggle
- Dockerized with PostgreSQL

## Stack

- Go
- PostgreSQL
- Telegram Bot API
- Covalent/GoldRush API (optional, via `COVALENT_API_KEY`)

## Quick Start

1. Copy env file:
   - `cp .env.example .env`
2. Fill:
   - `TELEGRAM_BOT_TOKEN`
   - `SUPERUSER_TELEGRAM_ID`
   - optional `COVALENT_API_KEY`
3. Run:
   - `docker compose up --build`

## Notes

- In Docker runtime (including Railway), container startup runs all SQL files in `/app/migrations` before launching the bot.
- In local `docker-compose`, Postgres also runs init SQL on first fresh volume via `docker-entrypoint-initdb.d`.
- If `COVALENT_API_KEY` is empty, polling runs but does not ingest transactions.
- Chain names should match Covalent format like `eth-mainnet`, `bsc-mainnet`, `base-mainnet`.

## Bot UX

- `/start` shows keyboard:
  - Wallets
  - Reports
  - Settings
  - Help
- Wallets:
  - Add Wallet (guided flow)
  - List Wallets
- Reports:
  - Hourly/Daily/Monthly/Yearly
- Settings:
  - Auto report frequency
  - Include unchanged wallets toggle
