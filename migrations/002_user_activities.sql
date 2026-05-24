CREATE TABLE IF NOT EXISTS user_activities (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  actor_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  actor_telegram_id BIGINT NOT NULL,
  actor_username TEXT,
  actor_role TEXT NOT NULL CHECK (actor_role IN ('user', 'superuser')),
  action TEXT NOT NULL,
  details TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_user_activities_created_at ON user_activities(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_user_activities_actor_tg_created ON user_activities(actor_telegram_id, created_at DESC);
