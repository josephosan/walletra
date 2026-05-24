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
for f in /app/migrations/*.sql; do
  [ -f "$f" ] || continue
  echo "[walletra] applying $f"
  psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f "$f"
done

echo "[walletra] starting bot..."
exec /usr/local/bin/bot
