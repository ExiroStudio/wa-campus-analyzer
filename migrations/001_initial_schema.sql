CREATE TABLE IF NOT EXISTS messages (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  wa_message_id   TEXT NOT NULL UNIQUE,
  chat_jid        TEXT NOT NULL,
  chat_name       TEXT,
  is_group        INTEGER NOT NULL DEFAULT 0,
  sender_jid      TEXT,
  sender_name     TEXT,
  body            TEXT,
  msg_type        TEXT,
  has_media       INTEGER NOT NULL DEFAULT 0,
  sent_at         DATETIME NOT NULL,
  received_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  raw_payload     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_chat_time ON messages(chat_jid, sent_at);

CREATE TABLE IF NOT EXISTS jobs (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  message_id    INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  status        TEXT NOT NULL DEFAULT 'pending', -- pending|running|done|failed|skipped
  attempts      INTEGER NOT NULL DEFAULT 0,
  run_after     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_error    TEXT,
  updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_jobs_pick ON jobs(status, run_after);

CREATE TABLE IF NOT EXISTS analyses (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  message_id       INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  category         TEXT NOT NULL,
  importance       INTEGER NOT NULL,
  action_required  INTEGER NOT NULL,
  deadline         DATETIME,
  event_start      DATETIME,
  event_location   TEXT,
  topic            TEXT,
  summary          TEXT NOT NULL,
  reasoning        TEXT,
  confidence       REAL NOT NULL,
  needs_review     INTEGER NOT NULL DEFAULT 0,
  model            TEXT NOT NULL,
  prompt_version   TEXT NOT NULL,
  raw_response     TEXT NOT NULL,
  tokens_in        INTEGER,
  tokens_out       INTEGER,
  created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  is_current       INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS idx_analyses_msg ON analyses(message_id, is_current);

CREATE TABLE IF NOT EXISTS user_state (
  message_id          INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
  status              TEXT NOT NULL DEFAULT 'open', -- open|done|dismissed (HANYA status lokal)
  category_override   TEXT,
  importance_override INTEGER,
  note                TEXT,
  updated_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS ignored_chats (
  chat_jid   TEXT PRIMARY KEY,
  reason     TEXT,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
