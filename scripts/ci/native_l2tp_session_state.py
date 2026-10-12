#!/usr/bin/env python3
"""Small state predicates shared by the native L2TP lifecycle harness."""

from __future__ import annotations

import argparse
import glob
import json
import sys
from pathlib import Path
from typing import Any


def _records(root: Path, pattern: str) -> list[dict[str, Any]]:
    records: list[dict[str, Any]] = []
    for name in sorted(glob.glob(str(root / pattern))):
        with open(name, encoding="utf-8") as source:
            value = json.load(source)
        if not isinstance(value, dict):
            raise ValueError(f"expected JSON object in {name}")
        records.append(value)
    return records


def active_records(root: Path) -> list[dict[str, Any]]:
    return _records(root, "l2tp/*/ppp-accounting/active/ppp*.json")


def snapshot_processes(root: Path) -> set[str]:
    return {
        str(record["Process"])
        for record in active_records(root)
        if record.get("Process")
    }


def has_new_active_session(root: Path, before: set[str], client_ip: str) -> bool:
    return any(
        record.get("ClientIP") == client_ip
        and record.get("Process")
        and str(record["Process"]) not in before
        for record in active_records(root)
    )


def matching_denial(root: Path, client_ip: str, reason: str) -> dict[str, Any] | None:
    for record in _records(root, "l2tp/*/ppp-accounting/admission-denials/*.json"):
        if record.get("client_ip") == client_ip and record.get("reason") == reason:
            return record
    return None


def denial_has_no_session_state(root: Path, client_ip: str, reason: str) -> bool:
    denial = matching_denial(root, client_ip, reason)
    if denial is None or not denial.get("process"):
        return False
    process = str(denial["process"])
    for record in active_records(root):
        if str(record.get("Process", "")) == process:
            return False
    for record in _records(root, "ppp-admission/pending/*.json"):
        if str(record.get("Process", "")) == process:
            return False
    return True


def main() -> int:
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers(dest="command", required=True)

    snapshot = sub.add_parser("snapshot")
    snapshot.add_argument("root", type=Path)

    new_session = sub.add_parser("has-new-session")
    new_session.add_argument("root", type=Path)
    new_session.add_argument("snapshot", type=Path)
    new_session.add_argument("client_ip")

    denied = sub.add_parser("denial-has-no-session-state")
    denied.add_argument("root", type=Path)
    denied.add_argument("client_ip")
    denied.add_argument("reason")

    args = parser.parse_args()
    if args.command == "snapshot":
        print(json.dumps(sorted(snapshot_processes(args.root))))
        return 0
    if args.command == "has-new-session":
        before = set(json.loads(args.snapshot.read_text(encoding="utf-8")))
        return 0 if has_new_active_session(args.root, before, args.client_ip) else 1
    if args.command == "denial-has-no-session-state":
        return 0 if denial_has_no_session_state(args.root, args.client_ip, args.reason) else 1
    return 2


if __name__ == "__main__":
    sys.exit(main())
