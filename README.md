# Walletra

Telegram bot: <a href="https://t.me/walletra_bot" target="_blank">@walletra_bot</a>

Telegram bot to track wallet transactions and generate hourly/daily/monthly/yearly reports.

## Features

- Role model: `user` and single `superuser` (from env)
- Add wallets with chain + token filters
- Poll wallets every 30 minutes (configurable)
- Scheduled auto reports per user settings
- On-demand reports from Telegram buttons
- Superuser command center (`/admin`, `/admin_users`, `/admin_activity`)
- Settings:
  - report frequency (`hourly`, `daily`, `monthly`, `yearly`)
  - include unchanged wallets toggle
- Dockerized with PostgreSQL

## Stack

- Go
- PostgreSQL
- Telegram Bot API
- Direct Polygon RPC queries (no explorer/indexer dependency)

## Quick Start

1. Copy env file:
   - `cp .env.example .env`
2. Fill:
   - `TELEGRAM_BOT_TOKEN`
   - `SUPERUSER_TELEGRAM_ID`
   - `POLYGON_DIRECT_PROVIDER_ENABLED=true`
   - `POLYGON_RPC_URL`
3. Run:
   - `docker compose up --build`

## Notes

- In Docker runtime (including Railway), container startup applies SQL files in `/app/migrations` once and tracks them in `schema_migrations`.
- In local `docker-compose`, Postgres also runs init SQL on first fresh volume via `docker-entrypoint-initdb.d`.
- Supported chain: `matic-mainnet` only.
- On startup, bot validates Polygon provider health and exits if Polygon RPC is unavailable.

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
- Superuser:
  - `/admin` for help center
  - `/admin_users [page]` for paginated user list
  - `/admin_activity <telegram_id> <YYYY-MM-DDTHH>` for one user in a specific UTC hour
