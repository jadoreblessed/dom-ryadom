-- An unfinished MAX dialog and pending notifications must survive a restart.
CREATE TABLE IF NOT EXISTS max_dialogs (
    user_id BIGINT PRIMARY KEY,
    stage SMALLINT NOT NULL,
    category TEXT NOT NULL DEFAULT '',
    building_id TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS max_notifications (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL,
    request_number TEXT NOT NULL,
    text TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS max_notifications_pending_idx
    ON max_notifications (next_attempt_at, id) WHERE delivered_at IS NULL;
