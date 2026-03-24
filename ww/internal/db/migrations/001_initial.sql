CREATE TABLE repos (
    name        TEXT PRIMARY KEY,
    source_path TEXT NOT NULL UNIQUE
);

CREATE TABLE workspaces (
    repo            TEXT NOT NULL REFERENCES repos(name),
    name            TEXT NOT NULL,
    path            TEXT NOT NULL,
    purpose         TEXT NOT NULL DEFAULT '',
    rev             TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'available'
        CHECK (status IN ('available', 'acquired')),
    acquired_by_pid INTEGER,
    provisioned     INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    acquired_at     TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (repo, name)
);
