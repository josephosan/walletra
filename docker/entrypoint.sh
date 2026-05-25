#!/bin/sh
set -e

if [ -z "$DATABASE_URL" ]; then
  echo "[walletra] DATABASE_URL is required"
  exit 1
fi

echo "[walletra] waiting for database..."
until psql "$DATABASE_URL" -c 'SELECT 1' >/dev/null 2>&1; do
  sleep 2
done

echo "[walletra] running migrations..."
# Track applied files so we don't re-run the same migration on every restart.
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 <<'SQL'
CREATE TABLE IF NOT EXISTS schema_migrations (
  filename TEXT PRIMARY KEY,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
SQL

for f in /app/migrations/*.sql; do
  [ -f "$f" ] || continue
  filename="$(basename "$f")"
  already_applied="$(psql "$DATABASE_URL" -t -A -c "SELECT 1 FROM schema_migrations WHERE filename='${filename}' LIMIT 1;")"
  if [ "$already_applied" = "1" ]; then
    echo "[walletra] skipping $filename (already applied)"
    continue
  fi

  echo "[walletra] applying $filename"
  psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f "$f"
  psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -c "INSERT INTO schema_migrations(filename) VALUES ('${filename}') ON CONFLICT (filename) DO NOTHING;"
done

echo "[walletra] starting bot..."
exec /usr/local/bin/bot
