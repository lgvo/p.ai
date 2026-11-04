#!/usr/bin/env python3
"""Bounded SQL readiness and schema initialization."""
import argparse
import json
import db

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("action", choices=["ready", "migrate", "inspect"])
args = parser.parse_args()
db.wait_ready()
if args.action == "migrate":
    db.migrate()
    print("notes schema ready (version 1)")
elif args.action == "inspect":
    print(json.dumps(db.list_notes()))
else:
    print("PostgreSQL SQL-ready")
