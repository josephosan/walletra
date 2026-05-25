CREATE TABLE IF NOT EXISTS polygon_indexer_state (
  chain TEXT PRIMARY KEY,
  last_indexed_block BIGINT NOT NULL,
  last_block_hash TEXT,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
