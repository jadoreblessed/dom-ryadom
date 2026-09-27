CREATE SEQUENCE IF NOT EXISTS request_number_seq START 1;

CREATE TABLE IF NOT EXISTS managing_orgs (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    phone           TEXT,
    emergency_phone TEXT
);

CREATE TABLE IF NOT EXISTS buildings (
    id              TEXT PRIMARY KEY,
    address         TEXT NOT NULL,
    managing_org_id TEXT NOT NULL REFERENCES managing_orgs(id)
);

CREATE TABLE IF NOT EXISTS requests (
    id           BIGSERIAL PRIMARY KEY,
    number       TEXT NOT NULL UNIQUE
                 DEFAULT ('REQ-' || lpad(nextval('request_number_seq')::text, 6, '0')),
    building_id  TEXT NOT NULL REFERENCES buildings(id),
    category     TEXT NOT NULL,
    description  TEXT NOT NULL,
    responsible  TEXT NOT NULL,
    next_step    TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'Новая',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS request_status_history (
    id          BIGSERIAL PRIMARY KEY,
    request_id  BIGINT NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
    from_status TEXT,
    to_status   TEXT NOT NULL,
    changed_by  TEXT NOT NULL,
    comment     TEXT,
    changed_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS dispatchers (
    login         TEXT PRIMARY KEY,
    password_hash TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_requests_status ON requests (status);
CREATE INDEX IF NOT EXISTS idx_requests_created ON requests (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_history_request ON request_status_history (request_id, changed_at);
