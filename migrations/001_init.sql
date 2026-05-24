CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TABLE IF NOT EXISTS users (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  telegram_id BIGINT UNIQUE NOT NULL,
  username TEXT,
  role TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('user', 'superuser')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS wallets (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  address TEXT NOT NULL,
  chain TEXT NOT NULL,
  base_coin TEXT,
  is_active BOOLEAN NOT NULL DEFAULT TRUE,
  last_polled_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE(user_id, address, chain)
);

CREATE TABLE IF NOT EXISTS wallet_token_filters (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  wallet_id UUID NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
  token_symbol TEXT NOT NULL,
  token_address TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE(wallet_id, token_symbol)
);

CREATE TABLE IF NOT EXISTS wallet_transactions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  wallet_id UUID NOT NULL REFERENCES wallets(id) ON DELETE CASCADE,
  tx_hash TEXT NOT NULL,
  chain TEXT NOT NULL,
  token_symbol TEXT,
  token_address TEXT,
  direction TEXT NOT NULL CHECK (direction IN ('buy', 'sell', 'transfer_in', 'transfer_out', 'unknown')),
  amount NUMERIC(38, 18) NOT NULL DEFAULT 0,
  amount_usd NUMERIC(38, 6),
  tx_timestamp TIMESTAMPTZ NOT NULL,
  raw_payload JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS user_settings (
  user_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  report_frequency TEXT NOT NULL DEFAULT 'daily' CHECK (report_frequency IN ('hourly', 'daily', 'monthly', 'yearly')),
  include_unchanged_wallets BOOLEAN NOT NULL DEFAULT FALSE,
  timezone TEXT NOT NULL DEFAULT 'UTC',
  next_report_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_wallet_transactions_wallet_time ON wallet_transactions(wallet_id, tx_timestamp DESC);
CREATE UNIQUE INDEX IF NOT EXISTS uq_wallet_transactions_dedupe
  ON wallet_transactions(wallet_id, tx_hash, COALESCE(token_symbol,''), tx_timestamp);
CREATE INDEX IF NOT EXISTS idx_wallets_user ON wallets(user_id);
