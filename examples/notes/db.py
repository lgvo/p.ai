"""Private PostgreSQL connections, schema, and transactional note storage."""
import pwd
import os
import time
from pathlib import Path

import psycopg
from psycopg import sql
from psycopg.rows import dict_row


class SchemaError(RuntimeError):
    """The stored schema requires explicit recovery, not automatic repair."""


def validate_schema(conn, *, allow_uninitialized=False):
    """Read-only readiness check; additional application columns are allowed."""
    required = {
        "notes_schema": {"version": "int4"},
        "notes": {"id": "int8", "body": "text", "status": "text", "word_count": "int4", "created_at": "timestamptz"},
        "jobs": {"id": "int8", "note_id": "int8", "completed_at": "timestamptz"},
    }
    columns = {}
    for row in conn.execute("""SELECT c.table_name, c.column_name, c.udt_schema, c.udt_name
        FROM information_schema.columns c JOIN information_schema.tables t
        USING (table_catalog, table_schema, table_name)
        WHERE c.table_schema='public' AND t.table_type='BASE TABLE'
          AND c.table_name IN ('notes_schema','notes','jobs')"""):
        columns.setdefault(row["table_name"], {})[row["column_name"]] = (row["udt_schema"], row["udt_name"])

    def check_table(table):
        if table not in columns:
            raise SchemaError(f"Damaged notes schema: required table {table} unavailable; preserve the database and recover explicitly")
        for column, kind in required[table].items():
            if columns[table].get(column) != ("pg_catalog", kind):
                raise SchemaError(f"Damaged notes schema: required column {table}.{column} must have type {kind}; preserve the database and recover explicitly")

    check_table("notes_schema")
    versions = [r["version"] for r in conn.execute("SELECT version FROM notes_schema")]
    if not versions and allow_uninitialized:
        return versions
    if versions != [1]:
        raise SchemaError(f"Unsupported notes schema version {versions}; preserve the database and migrate explicitly")
    check_table("notes")
    check_table("jobs")
    return versions


def state_dir():
    return Path(os.environ.get("P_NOTES_STATE", str(Path.home() / ".local/state/p-notes"))).absolute()


def connect(database="notes"):
    return psycopg.connect(host=str(state_dir() / "socket"), dbname=database,
                           user=pwd.getpwuid(os.getuid()).pw_name, hostaddr="", port=5432,
                           passfile=str(state_dir() / "unused-peer-password-file"), connect_timeout=2, row_factory=dict_row)


def wait_ready(timeout=25):
    deadline = time.monotonic() + timeout
    while True:
        try:
            with connect("postgres") as conn:
                conn.execute("SELECT 1")
            return
        except psycopg.OperationalError:
            if time.monotonic() >= deadline:
                raise RuntimeError("PostgreSQL did not become SQL-ready within the deadline") from None
            time.sleep(0.1)


def migrate():
    # Serialize CREATE DATABASE across repeated/concurrent setup commands.
    with connect("postgres") as conn:
        conn.autocommit = True
        conn.execute("SELECT pg_advisory_lock(730021)")
        try:
            if not conn.execute("SELECT 1 FROM pg_database WHERE datname='notes'").fetchone():
                conn.execute(sql.SQL("CREATE DATABASE {} ").format(sql.Identifier("notes")))
        finally:
            conn.execute("SELECT pg_advisory_unlock(730021)")
    with connect() as conn:
        conn.execute("SELECT pg_advisory_xact_lock(730022)")
        conn.execute("CREATE TABLE IF NOT EXISTS notes_schema (version integer PRIMARY KEY)")
        versions = validate_schema(conn, allow_uninitialized=True)
        if not versions:
            conn.execute("""CREATE TABLE notes (
                id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
                body text NOT NULL,
                status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processed')),
                word_count integer,
                created_at timestamptz NOT NULL DEFAULT now(),
                CHECK ((status='pending' AND word_count IS NULL) OR
                       (status='processed' AND word_count >= 0)))""")
            conn.execute("""CREATE TABLE jobs (
                id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
                note_id bigint NOT NULL UNIQUE REFERENCES notes(id),
                completed_at timestamptz)""")
            conn.execute("INSERT INTO notes_schema VALUES (1)")
            validate_schema(conn)


def create_note(body):
    if not isinstance(body, str) or not body.strip() or len(body) > 10000:
        raise ValueError("body must be a nonempty string of at most 10000 characters")
    with connect() as conn:
        note = conn.execute("INSERT INTO notes (body) VALUES (%s) RETURNING id, body, status, word_count",
                            (body,)).fetchone()
        conn.execute("INSERT INTO jobs (note_id) VALUES (%s)", (note["id"],))
    return note


def list_notes():
    with connect() as conn:
        return conn.execute("SELECT id, body, status, word_count FROM notes ORDER BY id").fetchall()
