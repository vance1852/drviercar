-- Fleet operations core: identities, sessions, campaigns, vehicles, shifts.
CREATE TABLE operators (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL,
    display_name  TEXT    NOT NULL DEFAULT '',
    role          TEXT    NOT NULL,
    salt          TEXT    NOT NULL,
    password_hash TEXT    NOT NULL,
    active        INTEGER NOT NULL DEFAULT 1,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

CREATE UNIQUE INDEX idx_operators_username ON operators (username);
CREATE INDEX idx_operators_role ON operators (role, active);

CREATE TABLE sessions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash   TEXT    NOT NULL,
    operator_id  INTEGER NOT NULL REFERENCES operators (id) ON DELETE CASCADE,
    role         TEXT    NOT NULL,
    issued_at    INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    revoked_at   INTEGER,
    last_seen_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX idx_sessions_token ON sessions (token_hash);
CREATE INDEX idx_sessions_operator ON sessions (operator_id, expires_at);

CREATE TABLE campaigns (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    code          TEXT    NOT NULL,
    city          TEXT    NOT NULL,
    status        TEXT    NOT NULL,
    planned_km    REAL    NOT NULL,
    committed_km  REAL    NOT NULL DEFAULT 0,
    window_start  INTEGER NOT NULL,
    window_end    INTEGER NOT NULL,
    owner_id      INTEGER NOT NULL REFERENCES operators (id),
    version       INTEGER NOT NULL DEFAULT 1,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    closed_at     INTEGER,
    cancel_reason TEXT    NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX idx_campaigns_code ON campaigns (code);
CREATE INDEX idx_campaigns_city_status ON campaigns (city, status);
CREATE INDEX idx_campaigns_window ON campaigns (window_start, window_end);

CREATE TABLE vehicles (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    plate          TEXT    NOT NULL,
    autonomy       TEXT    NOT NULL,
    status         TEXT    NOT NULL,
    home_depot     TEXT    NOT NULL,
    odometer_km    REAL    NOT NULL DEFAULT 0,
    sensor_profile TEXT    NOT NULL,
    version        INTEGER NOT NULL DEFAULT 1,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);

CREATE UNIQUE INDEX idx_vehicles_plate ON vehicles (plate);
CREATE INDEX idx_vehicles_depot_status ON vehicles (home_depot, status);

CREATE TABLE assignments (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    campaign_id     INTEGER NOT NULL REFERENCES campaigns (id) ON DELETE CASCADE,
    vehicle_id      INTEGER NOT NULL REFERENCES vehicles (id),
    operator_id     INTEGER NOT NULL REFERENCES operators (id),
    status          TEXT    NOT NULL,
    planned_km      REAL    NOT NULL,
    shift_start     INTEGER NOT NULL,
    shift_end       INTEGER NOT NULL,
    route           TEXT    NOT NULL,
    idempotency_key TEXT    NOT NULL,
    version         INTEGER NOT NULL DEFAULT 1,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    closed_at       INTEGER
);

CREATE UNIQUE INDEX idx_assignments_idempotency ON assignments (idempotency_key);
CREATE INDEX idx_assignments_campaign ON assignments (campaign_id, status);
CREATE INDEX idx_assignments_vehicle ON assignments (vehicle_id, status);
CREATE INDEX idx_assignments_operator ON assignments (operator_id, status);

CREATE TABLE drive_sessions (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    assignment_id  INTEGER NOT NULL REFERENCES assignments (id) ON DELETE CASCADE,
    vehicle_id     INTEGER NOT NULL REFERENCES vehicles (id),
    operator_id    INTEGER NOT NULL REFERENCES operators (id),
    status         TEXT    NOT NULL,
    started_at     INTEGER NOT NULL,
    ended_at       INTEGER,
    auto_km        REAL    NOT NULL DEFAULT 0,
    manual_km      REAL    NOT NULL DEFAULT 0,
    takeover_count INTEGER NOT NULL DEFAULT 0,
    version        INTEGER NOT NULL DEFAULT 1,
    updated_at     INTEGER NOT NULL
);

CREATE INDEX idx_drive_assignment ON drive_sessions (assignment_id, status);
CREATE INDEX idx_drive_vehicle ON drive_sessions (vehicle_id, status);

CREATE TABLE takeover_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    drive_id    INTEGER NOT NULL REFERENCES drive_sessions (id) ON DELETE CASCADE,
    occurred_at INTEGER NOT NULL,
    category    TEXT    NOT NULL,
    severity    INTEGER NOT NULL,
    manual_km   REAL    NOT NULL DEFAULT 0,
    description TEXT    NOT NULL,
    resolved    INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_takeover_drive ON takeover_events (drive_id, resolved);

CREATE TABLE settlements (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    campaign_id     INTEGER NOT NULL REFERENCES campaigns (id) ON DELETE CASCADE,
    assignment_id   INTEGER NOT NULL REFERENCES assignments (id) ON DELETE CASCADE,
    status          TEXT    NOT NULL,
    auto_km         REAL    NOT NULL DEFAULT 0,
    manual_km       REAL    NOT NULL DEFAULT 0,
    billable_km     REAL    NOT NULL DEFAULT 0,
    penalty_km      REAL    NOT NULL DEFAULT 0,
    critical_events INTEGER NOT NULL DEFAULT 0,
    business_day    TEXT    NOT NULL,
    computed_at     INTEGER NOT NULL,
    approved_by     INTEGER NOT NULL DEFAULT 0,
    note            TEXT    NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX idx_settlements_assignment ON settlements (assignment_id);
CREATE INDEX idx_settlements_campaign ON settlements (campaign_id, status);

CREATE TABLE audit_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    request_id  TEXT    NOT NULL DEFAULT '',
    operator_id INTEGER NOT NULL DEFAULT 0,
    object_type TEXT    NOT NULL,
    object_id   INTEGER NOT NULL DEFAULT 0,
    action      TEXT    NOT NULL,
    result      TEXT    NOT NULL,
    detail      TEXT    NOT NULL DEFAULT '{}',
    created_at  INTEGER NOT NULL
);

CREATE INDEX idx_audit_object ON audit_events (object_type, object_id);
CREATE INDEX idx_audit_request ON audit_events (request_id);

CREATE TABLE idempotency_keys (
    key           TEXT    NOT NULL,
    method        TEXT    NOT NULL,
    path          TEXT    NOT NULL,
    operator_id   INTEGER NOT NULL,
    request_hash  TEXT    NOT NULL,
    response_body TEXT    NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    PRIMARY KEY (key, method, path, operator_id)
);

CREATE INDEX idx_idempotency_created ON idempotency_keys (created_at);
