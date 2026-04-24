// Shared SQLite handle + schema migration. Two tables back the
// offline + queue features:
//   cached_docs     — per-tenant document cache (metadata + blob path)
//   pending_uploads — capture-to-upload queue with resume-on-network
//
// Keep all writes under `expo-sqlite`'s `execAsync` so the SQLite
// WAL fsyncs; losing a captured doc to an app kill is worse than a
// few ms of latency.

import * as SQLite from 'expo-sqlite'

let _db: SQLite.SQLiteDatabase | null = null

export async function db(): Promise<SQLite.SQLiteDatabase> {
  if (_db) return _db
  _db = await SQLite.openDatabaseAsync('vaultdms.db')
  await _db.execAsync(`
    PRAGMA journal_mode = WAL;

    CREATE TABLE IF NOT EXISTS cached_docs (
      tenant_id       TEXT    NOT NULL,
      document_id     TEXT    NOT NULL,
      metadata_json   TEXT    NOT NULL,
      blob_path       TEXT    NOT NULL,
      mime_type       TEXT,
      size_bytes      INTEGER NOT NULL,
      last_opened_at  INTEGER NOT NULL,
      PRIMARY KEY (tenant_id, document_id)
    );
    CREATE INDEX IF NOT EXISTS idx_cached_docs_lru
      ON cached_docs(last_opened_at);

    CREATE TABLE IF NOT EXISTS pending_uploads (
      id           TEXT PRIMARY KEY,
      tenant_id    TEXT    NOT NULL,
      local_path   TEXT    NOT NULL,
      filename     TEXT    NOT NULL,
      mime_type    TEXT    NOT NULL,
      size_bytes   INTEGER NOT NULL,
      status       TEXT    NOT NULL,
      attempts     INTEGER NOT NULL DEFAULT 0,
      last_error   TEXT,
      created_at   INTEGER NOT NULL,
      updated_at   INTEGER NOT NULL
    );
    CREATE INDEX IF NOT EXISTS idx_pending_uploads_status
      ON pending_uploads(status, created_at);
  `)
  return _db
}
