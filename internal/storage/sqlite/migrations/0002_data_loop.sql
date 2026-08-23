-- Data closed loop: capture batches, frames, shadow-mode triage, datasets, jobs.
CREATE TABLE capture_batches (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    vehicle_id     INTEGER NOT NULL REFERENCES vehicles (id),
    drive_id       INTEGER NOT NULL REFERENCES drive_sessions (id) ON DELETE CASCADE,
    upload_key     TEXT    NOT NULL,
    status         TEXT    NOT NULL,
    frame_count    INTEGER NOT NULL DEFAULT 0,
    accepted_count INTEGER NOT NULL DEFAULT 0,
    manifest       TEXT    NOT NULL,
    uploaded_at    INTEGER NOT NULL,
    validated_at   INTEGER,
    version        INTEGER NOT NULL DEFAULT 1,
    reject_reason  TEXT    NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX idx_capture_upload_key ON capture_batches (upload_key);
CREATE INDEX idx_capture_drive ON capture_batches (drive_id, status);
CREATE INDEX idx_capture_vehicle ON capture_batches (vehicle_id, uploaded_at);

CREATE TABLE capture_frames (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    batch_id      INTEGER NOT NULL REFERENCES capture_batches (id) ON DELETE CASCADE,
    sequence      INTEGER NOT NULL,
    sensor        TEXT    NOT NULL,
    payload_hash  TEXT    NOT NULL,
    quality_score REAL    NOT NULL,
    status        TEXT    NOT NULL,
    reason        TEXT    NOT NULL DEFAULT '',
    captured_at   INTEGER NOT NULL
);

CREATE UNIQUE INDEX idx_frames_batch_sequence ON capture_frames (batch_id, sequence);
CREATE INDEX idx_frames_status ON capture_frames (status, quality_score);

CREATE TABLE triage_tickets (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    batch_id    INTEGER NOT NULL REFERENCES capture_batches (id) ON DELETE CASCADE,
    drive_id    INTEGER NOT NULL REFERENCES drive_sessions (id) ON DELETE CASCADE,
    status      TEXT    NOT NULL,
    disposition TEXT    NOT NULL DEFAULT '',
    severity    INTEGER NOT NULL,
    assignee_id INTEGER NOT NULL DEFAULT 0,
    opened_at   INTEGER NOT NULL,
    deadline_at INTEGER NOT NULL,
    disposed_at INTEGER,
    conclusion  TEXT    NOT NULL DEFAULT '',
    version     INTEGER NOT NULL DEFAULT 1
);

CREATE INDEX idx_tickets_batch ON triage_tickets (batch_id, status);
CREATE INDEX idx_tickets_drive ON triage_tickets (drive_id, status);
CREATE INDEX idx_tickets_deadline ON triage_tickets (status, deadline_at);

CREATE TABLE datasets (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL,
    purpose     TEXT    NOT NULL DEFAULT '',
    status      TEXT    NOT NULL,
    frame_count INTEGER NOT NULL DEFAULT 0,
    owner_id    INTEGER NOT NULL REFERENCES operators (id),
    created_at  INTEGER NOT NULL,
    sealed_at   INTEGER,
    released_at INTEGER,
    version     INTEGER NOT NULL DEFAULT 1,
    seal_digest TEXT    NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX idx_datasets_name ON datasets (name);

CREATE TABLE dataset_members (
    dataset_id INTEGER NOT NULL REFERENCES datasets (id) ON DELETE CASCADE,
    frame_id   INTEGER NOT NULL REFERENCES capture_frames (id) ON DELETE CASCADE,
    added_at   INTEGER NOT NULL,
    PRIMARY KEY (dataset_id, frame_id)
);

CREATE INDEX idx_dataset_members_frame ON dataset_members (frame_id);

CREATE TABLE worker_jobs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    kind         TEXT    NOT NULL,
    payload      TEXT    NOT NULL DEFAULT '{}',
    status       TEXT    NOT NULL,
    attempts     INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    next_run_at  INTEGER NOT NULL,
    last_error   TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL,
    version      INTEGER NOT NULL DEFAULT 1
);

CREATE INDEX idx_jobs_due ON worker_jobs (status, next_run_at);
CREATE INDEX idx_jobs_kind ON worker_jobs (kind, status);
