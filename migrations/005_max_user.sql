ALTER TABLE requests
    ADD COLUMN IF NOT EXISTS max_user_id BIGINT;

CREATE INDEX IF NOT EXISTS requests_max_user_id_idx
    ON requests (max_user_id)
    WHERE max_user_id IS NOT NULL;
