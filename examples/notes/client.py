#!/usr/bin/env python3
"""Small session-local HTTP client: health, list, or add BODY."""
import argparse
import json
import urllib.error
import urllib.request

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("action", choices=["health", "list", "add"])
parser.add_argument("body", nargs="?")
args = parser.parse_args()
if args.action == "add" and args.body is None:
    parser.error("add requires BODY")
path = "/health" if args.action == "health" else "/notes"
data = json.dumps({"body": args.body}).encode() if args.action == "add" else None
request = urllib.request.Request("http://127.0.0.1:8000" + path, data=data,
                                 headers={"Content-Type": "application/json"})
try:
    with urllib.request.urlopen(request, timeout=5) as response:
        print(response.read().decode())
except urllib.error.HTTPError as exc:
    print(exc.read().decode())
    raise SystemExit(1)
except urllib.error.URLError as exc:
    print(f"HTTP endpoint unavailable: {exc.reason}")
    raise SystemExit(1)
