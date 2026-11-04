#!/usr/bin/env python3
"""Process durable jobs in one transaction; an interrupted claim rolls back."""
import argparse
import time
import db
import psycopg


def process_one(before_commit=None):
    with db.connect() as conn:
        job = conn.execute("""SELECT jobs.id, jobs.note_id, notes.body FROM jobs
            JOIN notes ON notes.id=jobs.note_id WHERE completed_at IS NULL
            ORDER BY jobs.id FOR UPDATE OF jobs SKIP LOCKED LIMIT 1""").fetchone()
        if job is None:
            return False
        count = len(job["body"].split())
        conn.execute("UPDATE notes SET status='processed', word_count=%s WHERE id=%s",
                     (count, job["note_id"]))
        conn.execute("UPDATE jobs SET completed_at=now() WHERE id=%s", (job["id"],))
        if before_commit:
            before_commit(job)
    print(f"processed job={job['id']} note={job['note_id']} word_count={count}", flush=True)
    return True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--once", action="store_true")
    parser.add_argument("--hold-before-commit", type=float, default=0,
                        help="controlled interruption fixture: log and pause inside the transaction")
    args = parser.parse_args()
    db.wait_ready()
    def hold(job):
        print(f"transaction open job={job['id']} note={job['note_id']}", flush=True)
        time.sleep(args.hold_before_commit)
    while True:
        try:
            processed = process_one(hold if args.hold_before_commit else None)
        except psycopg.Error as exc:
            print(f"worker database unavailable: {type(exc).__name__}", flush=True)
            if args.once:
                raise
            processed = False
        if args.once:
            return
        if not processed:
            time.sleep(0.2)


if __name__ == "__main__":
    main()
