-- MAX inbox and cursor are committed together, before the next poll.
CREATE TABLE IF NOT EXISTS max_poll_cursor (
    id SMALLINT PRIMARY KEY CHECK (id = 1),
    marker BIGINT
);

CREATE TABLE IF NOT EXISTS max_inbox (
    id BIGSERIAL PRIMARY KEY,
    event_key TEXT NOT NULL UNIQUE,
    payload JSONB NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS max_inbox_pending_idx ON max_inbox (id)
    WHERE processed_at IS NULL;

ALTER TABLE requests ADD COLUMN IF NOT EXISTS max_message_id TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS requests_max_message_id_idx ON requests (max_message_id);
ALTER TABLE max_dialogs ADD COLUMN IF NOT EXISTS last_message_id TEXT NOT NULL DEFAULT '';
