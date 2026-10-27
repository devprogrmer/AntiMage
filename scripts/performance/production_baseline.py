#!/usr/bin/env python3
"""Read-only, secret-redacting AntiMage runtime/accounting audit for Linux.

Example (SQLite):
  sudo python3 production_baseline.py --sqlite /var/lib/antimage/db.sqlite3 --sample-seconds 60
Example (auto-discover the panel database from the install .env):
  sudo python3 production_baseline.py --sample-seconds 60
Example (MySQL):
  sudo python3 production_baseline.py --mysql-defaults /root/.my.cnf --database antimage

MySQL credentials remain in the existing mode-0600 defaults file. No config,
process environment, usernames, identifiers, or raw daemon output is emitted.
Discovery reads only SQLALCHEMY_DATABASE_URL and the database-name portion of it;
a password embedded in a DSN is never copied into the report.
"""

import argparse
import csv
import hashlib
import io
import json
import os
from pathlib import Path
import re
import signal
import shutil
import subprocess
import sys
import time
from datetime import datetime, timezone


PROTOCOLS = ("xray", "vless", "vmess", "trojan", "shadowsocks", "hysteria", "openvpn",
             "wireguard", "amneziawg", "l2tp", "pptp", "ikev2", "anyconnect")
NATIVE_FIELDS = {"inbounds": "openvpn", "wg_inbounds": "wireguard",
                 "awg_inbounds": "amneziawg", "l2tp_inbounds": "l2tp",
                 "pptp_inbounds": "pptp", "ikev2_inbounds": "ikev2",
                 "anyconnect_inbounds": "anyconnect"}
MAX_FILE_BYTES = 64 * 1024 * 1024
MAX_DB_ROWS = 50000
COMMAND_DEADLINE = None
ENV_CANDIDATES = ("ANTIMAGE_ENV_FILE", "/opt/antimage/.env", "/etc/antimage/antimage.env")
APP_DIR_CANDIDATES = ("ANTIMAGE_APP_DIR", "/opt/antimage")


class CollectorStageError(RuntimeError):
    """Curated collector failure that always carries a secret-free stage name."""

    def __init__(self, stage, message, diagnostics=None, hint=None):
        super().__init__(message)
        self.stage = stage
        self.diagnostics = diagnostics
        self.hint = hint


class CollectorArgumentParser(argparse.ArgumentParser):
    """ArgumentParser whose usage errors are reported as collector JSON, not bare stderr."""

    def error(self, message):
        raise CollectorStageError("arguments", message)


def dotenv_assignments(path):
    """Parse KEY = value / KEY=value lines from an install .env without executing it."""
    try:
        raw = Path(path).read_text(encoding="utf-8", errors="replace")
    except OSError:
        return {}
    values = {}
    for line in raw.splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, _, value = line.partition("=")
        key = key.strip()
        if key.startswith("export "):
            key = key[len("export "):].strip()
        if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", key):
            continue
        value = value.strip().strip('"').strip("'")
        values.setdefault(key, value)
    return values


def discover_database(args):
    """Resolve the panel database read-only from the install .env when not given.

    Returns (status, note). Never copies a DSN password into the caller's args or
    the report: only an absolute SQLite path or a MySQL database name is derived.
    """
    env_paths = [os.environ.get(ENV_CANDIDATES[0])] + list(ENV_CANDIDATES[1:])
    app_dirs = [os.environ.get(APP_DIR_CANDIDATES[0])] + list(APP_DIR_CANDIDATES[1:])
    for app_dir in app_dirs:
        if app_dir:
            env_paths.append(str(Path(app_dir) / ".env"))
    seen = set()
    for candidate in env_paths:
        if not candidate or candidate in seen:
            continue
        seen.add(candidate)
        values = dotenv_assignments(candidate)
        if not values:
            continue
        url = values.get("SQLALCHEMY_DATABASE_URL") or values.get("DATABASE_URL") or ""
        url = url.strip()
        lower = url.lower()
        base = str(Path(candidate).resolve())
        if lower.startswith("sqlite"):
            # Django convention: sqlite:////abs/path (4 slashes) is absolute;
            # sqlite:///relative/path (3 slashes) is relative to the app dir.
            if "://" not in url:
                continue
            rest = url.split("://", 1)[1]
            rest = rest.split("?", 1)[0]
            if not rest or rest.lstrip("/") in ("", ":memory:"):
                continue
            absolute = rest.startswith("//")
            relative = rest.lstrip("/")
            path = Path("/" + relative) if absolute else Path(candidate).resolve().parent / relative
            args.sqlite = str(path)
            return "ok", f"sqlite path resolved from {base}"
        if lower.startswith("mysql") or lower.startswith("mariadb"):
            name = url.rsplit("/", 1)[-1].split("?", 1)[0].strip()
            if re.fullmatch(r"[A-Za-z0-9_]{1,64}", name):
                args.database = name
                return "needs_credentials", (
                    f"MySQL database name {name} resolved from {base}; supply "
                    "--mysql-defaults <mode-0600 client cnf> to read table state")
    return "not_found", ("no install .env with SQLALCHEMY_DATABASE_URL was found; pass --sqlite "
                         "<absolute panel db path> or --mysql-defaults <mode-0600 cnf>")


def safe_message(value, limit=240):
    """First line only, truncated, with credential-shaped tokens masked."""
    text = str(value or "").strip()
    text = text.splitlines()[0] if text else ""
    text = re.sub(r"(?i)\b(password|passwd|secret|token|apikey|api_key|authorization|uuid)\b\s*[:=]\s*\S+",
                  r"\1=<redacted>", text)
    text = re.sub(r"[0-9a-fA-F]{32,}", "<redacted>", text)
    text = re.sub(r"[A-Za-z0-9+/=_]{28,}", "<redacted>", text)
    return text[:limit]


def error_payload(exc):
    """Valid JSON error document. Never contains DB rows, credentials or daemon output."""
    message = safe_message(exc)
    return {
        "schema_version": 2,
        "collector": "production_baseline",
        "status": "error",
        "error": "collector could not complete",
        "stage": getattr(exc, "stage", None) or "unknown",
        "exception_type": type(exc).__name__,
        "message": message,
        "hint": safe_message(getattr(exc, "hint", None)) or None,
        "diagnostics": getattr(exc, "diagnostics", None),
        "captured_stderr": message or None,
        "generated_utc": utc_now(),
        "redaction": ("message is the first line only, truncated to 240 characters, with "
                      "credential-shaped tokens masked; no DB rows, usernames, keys or raw daemon replies"),
    }


def write_json_file(path, text):
    """Replace the target atomically so an interrupt cannot leave a partial report."""
    temporary = f"{path}.tmp.{os.getpid()}"
    with open(temporary, "w", encoding="utf-8") as handle:
        handle.write(text + "\n")
        handle.flush()
        os.fsync(handle.fileno())
    os.replace(temporary, path)


def install_signal_handlers():
    """Turn SIGHUP/SIGTERM/SIGINT into a caught failure so a JSON document is still written."""
    def convert(signum, _frame):
        try:
            label = signal.Signals(signum).name
        except ValueError:
            label = f"signal {signum}"
        raise CollectorStageError("interrupted",
                                  f"collector interrupted by {label}; "
                                  "partial observations are not a valid report",
                                  hint="rerun under setsid/nohup if the SSH session may disconnect")

    for name in ("SIGHUP", "SIGTERM", "SIGINT"):
        number = getattr(signal, name, None)
        if number is None:
            continue
        try:
            signal.signal(number, convert)
        except (OSError, ValueError, RuntimeError):
            pass


def protocol_name(value):
    # Never emit an unrecognized config/DB value: it might contain an identity.
    value = str(value or "").lower().strip()
    value = {"wg": "wireguard", "awg": "amneziawg", "ocserv": "anyconnect",
             "ss": "shadowsocks", "shadowsocks2022": "shadowsocks",
             "shadowsocks-2022": "shadowsocks"}.get(value, value)
    return value if value in PROTOCOLS else "unknown"


def field(obj, key, default=None):
    """Go disk structs include both tagged snake_case and untagged PascalCase."""
    for candidate in (key, "".join(x.capitalize() for x in key.split("_")),
                      {"user_id": "UserID", "batch_id": "BatchID", "inbound_tag": "InboundTag"}.get(key, key)):
        if candidate in obj:
            return obj[candidate]
    return default


def uint(value):
    if isinstance(value, bool):
        return 0
    try:
        return max(0, int(value or 0))
    except (ValueError, TypeError, OverflowError):
        return 0


def read_json(path):
    # No write-capable open, package install, service action or raw exception output.
    if COMMAND_DEADLINE is not None and time.monotonic() >= COMMAND_DEADLINE:
        return None, "observation_budget_exhausted"
    try:
        with open(path, "rb") as stream:
            raw = stream.read(MAX_FILE_BYTES + 1)
        if len(raw) > MAX_FILE_BYTES:
            return None, "size_limit"
        value = json.loads(raw)
        if not isinstance(value, dict):
            return None, "unsupported_shape"
        return value, "ok"
    except FileNotFoundError:
        return None, "missing"
    except (OSError, ValueError, RecursionError):
        return None, "unreadable_or_invalid"


def decoded_json(value):
    if isinstance(value, dict):
        return value
    try:
        result = json.loads(value or "{}")
        return result if isinstance(result, dict) else {}
    except (ValueError, TypeError):
        return {}


def resolved_protocol(protocol, tag, inventory):
    value = protocol_name(protocol)
    if value == "xray":
        candidate = inventory["inbounds"].get(tag, "xray")
        return candidate if candidate not in NATIVE_FIELDS.values() and candidate in PROTOCOLS else "xray"
    return value


def owner_key(protocol, row, inventory=None):
    uid = uint(field(row, "user_id"))
    tag = str(field(row, "inbound_tag", "") or "")
    value = resolved_protocol(protocol, tag, inventory) if inventory is not None else protocol_name(protocol)
    return value, uid, tag


def parse_reflection_key(key):
    try:
        protocol, end = json.JSONDecoder().raw_decode(key)
        if key[end:end + 1] != ":":
            return None
        tag = json.loads(key[end + 1:])
        return protocol_name(protocol), str(tag)
    except (ValueError, TypeError):
        return None


def runtime_inventory(data_dir):
    snap, status = read_json(Path(data_dir) / "accounting/runtime-policy.json")
    result = {"status": status, "rows": {}, "inbounds": {}, "native": {}, "node_id": 0,
              "revision": None, "file_mtime_ns": None, "xray_api_port": None}
    if snap is None:
        return result
    native = decoded_json(snap.get("native_json"))
    config = decoded_json(snap.get("config_json"))
    result["node_id"] = uint(native.get("session_callback", {}).get("node_id"))
    result["revision"] = uint(snap.get("revision"))
    try:
        result["file_mtime_ns"] = (Path(data_dir) / "accounting/runtime-policy.json").stat().st_mtime_ns
    except OSError:
        pass
    for inbound in config.get("inbounds", []) or []:
        if isinstance(inbound, dict):
            if str(inbound.get("tag", "")).strip() == "API_INBOUND":
                port = uint(inbound.get("port"))
                if 0 < port <= 65535:
                    result["xray_api_port"] = port
                continue
            result["inbounds"][str(inbound.get("tag", ""))] = protocol_name(inbound.get("protocol"))
    for policy in native.get("xray_policies", []) or []:
        if not isinstance(policy, dict):
            continue
        tag = str(policy.get("inbound_tag", ""))
        protocol = protocol_name(policy.get("protocol") or result["inbounds"].get(tag))
        key = (protocol, uint(policy.get("user_id")), tag)
        if key[1]:
            result["rows"][key] = policy
    for field_name, protocol in NATIVE_FIELDS.items():
        for inbound in native.get(field_name, []) or []:
            tag = str(inbound.get("tag", ""))
            result["inbounds"][tag] = protocol
            result["native"][(protocol, tag)] = inbound
            for user in inbound.get("peers" if protocol in ("wireguard", "amneziawg") else "users", []) or []:
                key = (protocol, uint(user.get("user_id")), tag)
                if key[1]:
                    result["rows"][key] = user
    return result


def eligible(row, now=None):
    now = time.time() if now is None else now
    return (str(row.get("status", "")).lower() in ("active", "on_hold")
            and not row.get("admin_disabled_at") and not row.get("service_limit_disabled_at")
            and (not uint(row.get("expire")) or uint(row.get("expire")) > now)
            and (not uint(row.get("data_limit")) or uint(row.get("used_traffic")) < uint(row.get("data_limit"))))


class ReadOnlyDB:
    """Allowlisted SELECT projections only. Never read credentials or proxy settings."""
    TABLES = {
        "users": ("id", "status", "used_traffic", "data_limit", "expire", "service_id",
                  "admin_disabled_at", "service_limit_disabled_at"),
        "proxies": ("id", "user_id", "type"),
        "hosts": ("id", "inbound_tag", "is_disabled"),
        "service_hosts": ("service_id", "host_id"),
        "exclude_inbounds_association": ("proxy_id", "inbound_tag"),
        "inbounds": ("id", "tag"),
        "node_wireguard_usage_reflection": ("node_id", "user_id", "batch_id", "ledger_json"),
        "node_usage_user_queue": ("node_id", "user_id", "used_traffic", "processed_at"),
    }

    def __init__(self, args):
        self.db, self.args, self.status, self.queries = None, args, {}, 0
        if args.sqlite:
            try:
                import sqlite3
                self.db = sqlite3.connect(Path(args.sqlite).resolve().as_uri() + "?mode=ro", uri=True, timeout=2)
                self.db.execute("PRAGMA query_only=ON")
                deadline = time.monotonic() + 10
                self.db.set_progress_handler(lambda: int(time.monotonic() > deadline), 10000)
            except Exception:
                self.status["connection"] = "unavailable"

    def rows(self, table, node_id=0):
        self.queries += 1
        try:
            if self.db:
                columns = {r[1] for r in self.db.execute("PRAGMA table_info(" + table + ")")}
                selected = [x for x in self.TABLES[table] if x in columns]
                if not selected:
                    raise ValueError
                where = " WHERE node_id=" + str(int(node_id)) if node_id and "node_id" in columns else ""
                rows = self.db.execute("SELECT " + ",".join(selected) + " FROM " + table + where
                                       + " LIMIT " + str(MAX_DB_ROWS + 1)).fetchall()
            elif self.args.mysql_defaults:
                path = Path(self.args.mysql_defaults)
                if path.stat().st_mode & 0o077 or not shutil.which("mysql"):
                    raise ValueError
                base = ["mysql", "--defaults-file=" + str(path), "--connect-timeout=3",
                        "--init-command=SET SESSION TRANSACTION READ ONLY", "--batch",
                        "--skip-column-names", "--raw", "--database", self.args.database]
                meta = command(base + ["--execute", "SET SESSION TRANSACTION READ ONLY; SHOW COLUMNS FROM " + table], 5)
                if meta is None or meta.returncode:
                    raise ValueError
                columns = {line.split("\t")[0] for line in meta.stdout.splitlines()}
                selected = [x for x in self.TABLES[table] if x in columns]
                if not selected:
                    raise ValueError
                where = " WHERE node_id=" + str(int(node_id)) if node_id and "node_id" in columns else ""
                # JSON arrays preserve embedded tabs/newlines in watermark JSON; raw identities stay internal.
                query = "SET SESSION TRANSACTION READ ONLY; START TRANSACTION READ ONLY; SELECT JSON_ARRAY(" + ",".join(selected) + ") FROM " + table + where + " LIMIT " + str(MAX_DB_ROWS + 1) + "; ROLLBACK"
                reply = command(base + ["--execute", query], 10)
                if reply is None or reply.returncode:
                    raise ValueError
                rows = [json.loads(line) for line in reply.stdout.splitlines() if line]
            else:
                raise ValueError
            self.status[table] = "truncated" if len(rows) > MAX_DB_ROWS else "ok"
            return [dict(zip(selected, row)) for row in rows[:MAX_DB_ROWS]]
        except Exception:
            self.status[table] = "unavailable"
            return []

    def close(self):
        if self.db:
            self.db.close()


def db_inventory(args, inventory):
    db = ReadOnlyDB(args)
    node_id = args.node_id or inventory["node_id"]
    identity_mismatch = bool(args.node_id and inventory["node_id"] and args.node_id != inventory["node_id"])
    if identity_mismatch:
        node_id = 0
    result = {"users": {}, "expected": set(), "assigned": {p: set() for p in PROTOCOLS},
              "receipts": {}, "queue": [], "status": db.status, "query_count": 0,
              "node_scoped": bool(node_id), "node_identity_mismatch": identity_mismatch,
              "expectation_complete": False}
    try:
        result["users"] = {uint(r.get("id")): r for r in db.rows("users")}
        proxies = db.rows("proxies")
        hosts = db.rows("hosts")
        links = db.rows("service_hosts")
        excludes = {(uint(r.get("proxy_id")), r.get("inbound_tag")) for r in db.rows("exclude_inbounds_association")}
        db.rows("inbounds")
        enabled_hosts = {uint(r.get("id")): r.get("inbound_tag") for r in hosts if not uint(r.get("is_disabled"))}
        services = {}
        for link in links:
            tag = enabled_hosts.get(uint(link.get("host_id")))
            if tag in inventory["inbounds"]:
                services.setdefault(uint(link.get("service_id")), set()).add(tag)
        user_proxies = {}
        for proxy in proxies:
            user_proxies.setdefault(uint(proxy.get("user_id")), []).append(proxy)
            protocol = protocol_name(proxy.get("type"))
            if protocol in result["assigned"]:
                result["assigned"][protocol].add(uint(proxy.get("user_id")))
        for uid, user in result["users"].items():
            for tag in services.get(uint(user.get("service_id")), ()):
                protocol = inventory["inbounds"][tag]
                if protocol not in PROTOCOLS:
                    continue
                permitted = protocol in NATIVE_FIELDS.values() or any(
                    protocol_name(p.get("type")) == protocol and (uint(p.get("id")), tag) not in excludes
                    for p in user_proxies.get(uid, ()))
                if permitted:
                    result["assigned"][protocol].add(uid)
                    if eligible(user):
                        result["expected"].add((protocol, uid, tag))
        required = ("users", "hosts", "service_hosts", "proxies", "exclude_inbounds_association")
        result["expectation_complete"] = all(db.status.get(x) == "ok" for x in required) and inventory["status"] == "ok"
        if node_id:
            for row in db.rows("node_wireguard_usage_reflection", node_id):
                result["receipts"][uint(row.get("user_id"))] = row
            result["queue"] = db.rows("node_usage_user_queue", node_id)
        result["query_count"] = db.queries
        return result
    finally:
        db.close()


def offline_owner(key, protocol):
    prefix = "\x00offline-total\x00"
    if not key.startswith(prefix):
        return None
    uid, sep, tag = key[len(prefix):].partition("\x00")
    return (protocol, uint(uid), tag) if sep and uint(uid) else None


def xray_identity_index(inventory):
    labels = {}
    for key, row in inventory["rows"].items():
        label = row.get("email")
        if label and key[0] not in NATIVE_FIELDS.values():
            labels.setdefault(str(label), set()).add(key)
    # An ambiguous identity must be flagged, never arbitrarily assigned.
    return {label: next(iter(keys)) for label, keys in labels.items() if len(keys) == 1}


def accounting_inventory(data_dir, inventory):
    result = {"totals": {}, "native": {}, "pending": {}, "state_status": {}, "acked": {},
              "ledger": {}, "epoch": None, "duplicate_owners": 0, "duplicate_children": 0,
              "invalid_counters": 0, "anchor_status": "not_referenced", "anchor_bytes": 0,
              "ledger_version": None, "legacy_deliveries": 0, "state_bytes": 0, "files": {}}
    emails = xray_identity_index(inventory)
    for dirname in ("xray",) + tuple(NATIVE_FIELDS.values()):
        path = Path(data_dir) / dirname / "usage-state.json"
        state, status = read_json(path)
        result["state_status"][dirname] = status
        if state is None:
            continue
        try:
            info = path.stat()
            result["files"][dirname] = (info.st_mtime_ns, info.st_size)
            result["state_bytes"] += info.st_size
        except OSError:
            pass
        result["acked"][dirname] = state.get("last_acked_batch_id")
        # offline-total entries are cumulative logical counters, not raw session totals.
        for label, value in (state.get("baseline", {}) or {}).items():
            key = offline_owner(label, dirname)
            if key:
                result["totals"][key] = uint(value)
        for label, counter in (state.get("counters", {}) or {}).items():
            email, sep, direction = label.rpartition(":")
            key = emails.get(email)
            if key and sep and direction in ("uplink", "downlink") and isinstance(counter, dict):
                result["totals"][key] = result["totals"].get(key, 0) + uint(counter.get("total"))
                result["native"][key] = result["native"].get(key, 0) + uint(counter.get("native"))
                result["invalid_counters"] += int(uint(counter.get("native")) > uint(counter.get("total")))
        pending = state.get("pending") or {}
        batch = field(pending, "batch_id", "")
        for row in field(pending, "samples", []) or []:
            key = owner_key(dirname, row, inventory)
            if dirname == "xray":
                matches = [k for k in inventory["rows"] if k[1:] == key[1:]]
                key = matches[0] if len(matches) == 1 else key
            if key[1]:
                result["pending"][key] = (batch, uint(field(row, "value")))
    state, result["ledger_status"] = read_json(Path(data_dir) / "accounting/deliveries.json")
    if state is None:
        return result
    # The on-disk layout decides which NodeAgent reflection path is live: v1
    # keeps inline deliveries and builds receipts from the bounded batch list,
    # v2 carries a cumulative owner ledger. Without this the reader cannot tell
    # a v2 ledger from a v1 file that happens to be small.
    result["ledger_version"] = state.get("version")
    ledger = state.get("ledger") or {}
    result["epoch"] = ledger.get("epoch")
    deliveries = state.get("deliveries", []) or []
    anchor_hash = state.get("legacy_anchors_hash")
    if anchor_hash and re.fullmatch(r"[a-f0-9]{64}", str(anchor_hash)):
        path = Path(data_dir) / "accounting" / ("deliveries-v1-" + anchor_hash + ".json")
        anchors, status = read_json(path)
        result["anchor_status"] = status
        if anchors is not None:
            try:
                with open(path, "rb") as stream:
                    raw = stream.read(MAX_FILE_BYTES + 1)
                result["anchor_bytes"] = len(raw)
                result["anchor_status"] = "ok" if hashlib.sha256(raw).hexdigest() == anchor_hash else "checksum_mismatch"
            except OSError:
                result["anchor_status"] = "unreadable"
            deliveries = anchors.get("deliveries", []) or []
    elif anchor_hash:
        result["anchor_status"] = "invalid_reference"
    result["legacy_deliveries"] = len(deliveries)
    seen = set()
    legacy_totals = {}
    for delivery in deliveries:
        for row in delivery.get("rows", []) or []:
            key = owner_key(row.get("protocol"), row, inventory)
            child = (key, row.get("child_id"))
            result["duplicate_children"] += int(child in seen)
            seen.add(child)
            legacy_totals[key] = legacy_totals.get(key, 0) + uint(row.get("raw"))
    if state.get("version") == 1:
        result["ledger"] = legacy_totals
    else:
        for row in ledger.get("owners", []) or []:
            key = owner_key(row.get("protocol"), row, inventory)
            result["duplicate_owners"] += int(key in result["ledger"])
            total = uint(row.get("total"))
            result["ledger"][key] = total
            result["invalid_counters"] += int(uint(row.get("legacy_total")) != legacy_totals.get(key, 0)
                or uint(row.get("last_raw")) > total or uint(row.get("legacy_total")) > total)
    return result


def live_result(source):
    return {"available": False, "complete": False, "source": source, "loaded": None,
            "active": set(), "sessions": 0, "series": {}, "unmapped_sessions": 0,
            "unmapped_accounts": 0, "stale_records": 0, "configured_accounts": set(),
            "observed_monotonic": time.monotonic()}


def user_lookup(inventory, protocol, tag=None):
    lookup = {}
    for key, row in inventory["rows"].items():
        if key[0] == protocol and (tag is None or key[2] == tag):
            for label in (row.get("vpn_username"), row.get("username"), row.get("email"), row.get("public_key")):
                if label:
                    lookup.setdefault(str(label), set()).add(key)
    return {label: next(iter(keys)) for label, keys in lookup.items() if len(keys) == 1}


def helper_configs(data_dir, protocol):
    root = Path(data_dir) / protocol
    # Match the actual managed layouts, including WG/AWG's intermediate
    # directory. A one-level glob silently misses all their managed accounts.
    patterns = ("*/usage-helper.json",)
    if protocol == "wireguard":
        patterns += ("inbounds/*/usage-helper.json",)
    elif protocol == "amneziawg":
        patterns += ("runtime/*/usage-helper.json",)
    paths = {path for pattern in patterns for path in root.glob(pattern)}
    for path in sorted(paths)[:100]:
        config, status = read_json(path)
        if status == "ok":
            yield path.parent, config


def read_text_limited(path):
    try:
        with open(path, "rb") as stream:
            raw = stream.read(MAX_FILE_BYTES + 1)
        return raw.decode("utf-8", errors="replace") if len(raw) <= MAX_FILE_BYTES else None
    except OSError:
        return None


def live_wireguard(data_dir, inventory, protocol):
    result = live_result("wg/awg show all dump (read-only)")
    binary = "wg" if protocol == "wireguard" else "awg"
    if not shutil.which(binary):
        return result
    response = command([binary, "show", "all", "dump"], 5)
    if response is None or response.returncode:
        return result
    by_interface = {}
    for _, config in helper_configs(data_dir, protocol):
        tag = str(config.get("inbound_tag", ""))
        iface = str(config.get("interface_name", ""))
        # Some legacy helpers lack the interface name; resolve via runtime manifest.
        if iface:
            by_interface[iface] = (tag, config.get("peers", {}))
    result["available"] = True
    result["complete"] = bool(by_interface)
    result["loaded"] = set()
    for line in response.stdout.splitlines():
        columns = line.split("\t")
        # With "all", peer columns include the interface before public key.
        if len(columns) != 9:
            continue
        iface, public_key = columns[:2]
        mapping = by_interface.get(iface)
        if mapping is None:
            # Ignore unmanaged interfaces, but disclose the observation gap.
            result["complete"] = False
            result["unmapped_accounts"] += 1
            continue
        tag, peers = mapping
        uid = uint(peers.get(public_key))
        if not uid:
            result["unmapped_accounts"] += 1
            continue
        key = (protocol, uid, tag)
        result["loaded"].add(key)
        result["series"][(iface, public_key)] = (key, uint(columns[6]) + uint(columns[7]))
        handshake = uint(columns[5])
        if handshake and 0 <= time.time() - handshake <= 180:
            result["active"].add(key)
            result["sessions"] += 1
    if not by_interface:
        # The dump was readable, but no owner mapping was available. Zero
        # matched accounts/sessions cannot be reported as an observed zero.
        result["loaded"] = None
        result["available"] = False
        result["unavailable_reason"] = "managed_owner_mapping_unavailable"
    return result


def live_openvpn(data_dir, inventory):
    result = live_result("OpenVPN v3 status files; configured account count is not daemon proof")
    files = list(helper_configs(data_dir, "openvpn"))
    result["complete"] = bool(files)
    for root, config in files:
        tag, users = str(config.get("inbound_tag", "")), config.get("users", {})
        result["configured_accounts"].update(("openvpn", uint(uid), tag) for uid in users.values() if uint(uid))
        status_path = root / "status.tsv"
        # Only use the managed file; never follow arbitrary config paths.
        raw = read_text_limited(status_path)
        if raw is None:
            result["complete"] = False
            continue
        try:
            age = time.time() - status_path.stat().st_mtime
        except OSError:
            age = 1e9
        if age > 120:
            result["stale_records"] += 1
            result["complete"] = False
            continue
        result["available"] = True
        header = None
        for columns in csv.reader(io.StringIO(raw), delimiter="\t"):
            if columns[:2] == ["HEADER", "CLIENT_LIST"]:
                header = columns[2:]
            elif columns and columns[0] == "CLIENT_LIST" and header:
                row = dict(zip(header, columns[1:]))
                name = row.get("Username")
                if not name or name == "UNDEF":
                    name = row.get("Common Name")
                uid = uint(users.get(name))
                result["sessions"] += 1
                if not uid:
                    result["unmapped_sessions"] += 1
                    continue
                key = ("openvpn", uid, tag)
                result["active"].add(key)
                identity = (tag, name, row.get("Client ID"), row.get("Connected Since (time_t)"))
                result["series"][identity] = (key, uint(row.get("Bytes Received")) + uint(row.get("Bytes Sent")))
    return result


def live_ppp(data_dir, inventory, protocol):
    result = live_result("PPP durable session records validated against live non-zombie PID/start/boot and interface")
    result["complete"] = True
    boot = read_text_limited("/proc/sys/kernel/random/boot_id")
    configs = list(helper_configs(data_dir, protocol))
    result["available"] = bool(configs) and bool(boot)
    for root, config in configs:
        tag = str(config.get("inbound_tag", ""))
        result["configured_accounts"].update((protocol, uint(uid), tag) for uid in config.get("users", {}).values() if uint(uid))
        final_ids = set()
        final_paths = sorted((root / "ppp-accounting/final").glob("*.json"))
        if len(final_paths) > 10000:
            result["complete"] = False
        for path in final_paths[:10000]:
            record, status = read_json(path)
            if status == "ok" and field(record, "final", False):
                final_ids.add((field(record, "id", record.get("ID")), field(record, "process")))
        paths = sorted((root / "ppp-accounting/active").glob("*.json"))
        seen_sessions = set()
        if len(paths) > 10000:
            result["complete"] = False
        for path in paths[:10000]:
            record, status = read_json(path)
            if status != "ok":
                result["complete"] = False
                continue
            identity = str(field(record, "process", ""))
            parts = identity.split(":")
            iface = str(field(record, "interface", ""))
            valid = (len(parts) == 3 and boot and parts[0] == boot.strip()
                     and parts[1].isdigit() and re.fullmatch(r"ppp\d+", iface))
            raw_stat = read_proc(uint(parts[1]), "stat").decode(errors="replace") if valid else ""
            stat_fields = raw_stat.rsplit(")", 1)[-1].split() if ")" in raw_stat else []
            valid = valid and len(stat_fields) > 19 and stat_fields[0] not in ("Z", "X") and stat_fields[19] == parts[2]
            uid = uint(field(record, "user_id"))
            interface_index = read_text_limited(Path("/sys/class/net") / iface / "ifindex") if valid else None
            valid = valid and interface_index is not None and field(record, "identity") == boot.strip() + ":" + interface_index.strip()
            finalized = (field(record, "id", record.get("ID")), identity) in final_ids
            if not valid or field(record, "final", False) or finalized or not uid:
                result["stale_records"] += 1
                continue
            if (iface, identity) in seen_sessions:
                result["stale_records"] += 1
                continue
            seen_sessions.add((iface, identity))
            rx = read_text_limited(Path("/sys/class/net") / iface / "statistics/rx_bytes")
            tx = read_text_limited(Path("/sys/class/net") / iface / "statistics/tx_bytes")
            if rx is None or tx is None:
                result["stale_records"] += 1
                continue
            key = (protocol, uid, str(field(record, "inbound_tag", tag)))
            result["active"].add(key)
            result["sessions"] += 1
            result["series"][(iface, identity)] = (key, uint(rx) + uint(tx))
    return result


def raw_blocks(raw):
    """Parse the pinned swanctl raw brace format without exposing its contents."""
    position = 0
    while position < len(raw):
        match = re.search(r"([^\s{}]+)\s*\{", raw[position:])
        if not match:
            return
        start = position + match.end()
        depth, end = 1, start
        while end < len(raw) and depth:
            depth += int(raw[end] == "{") - int(raw[end] == "}")
            end += 1
        if depth:
            return
        yield match.group(1), raw[start:end - 1]
        position = end


def raw_scalar(raw, key):
    match = re.search(r"(?:^|\s)" + re.escape(key) + r"=([^\s{}\[\]]+)", raw)
    return match.group(1) if match else ""


def live_ikev2(data_dir, inventory):
    result = live_result("swanctl --list-sas --raw; EAP identity required for attribution")
    response = command(["swanctl", "--list-sas", "--raw"], 5)
    if response is None or response.returncode:
        return result
    result["available"], result["complete"] = True, True
    lookup = user_lookup(inventory, "ikev2")
    for _, event in raw_blocks(response.stdout):
        for name, body in raw_blocks(event):
            header = body.split("child-sas", 1)[0]
            if raw_scalar(header, "state") != "ESTABLISHED":
                continue
            result["sessions"] += 1
            # Do not use remote-id="client" as the EAP username.
            key = lookup.get(raw_scalar(header, "remote-eap-id"))
            if key is None:
                result["unmapped_sessions"] += 1
                continue
            result["active"].add(key)
            total = sum(uint(value) for value in re.findall(r"(?:bytes-in|bytes-out)=(\d+)", body))
            result["series"][(name, raw_scalar(header, "uniqueid"), raw_scalar(header, "initiator-spi"))] = (key, total)
    return result


def live_anyconnect(data_dir, inventory):
    result = live_result("occtl -j show users via managed control sockets")
    configs = list(helper_configs(data_dir, "anyconnect"))
    result["complete"] = bool(configs)
    for _, config in configs:
        socket_path = str(config.get("socket_path", ""))
        if not socket_path.startswith("/"):
            result["complete"] = False
            continue
        reply = command(["occtl", "-s", socket_path, "-j", "show", "users"], 5)
        if reply is None or reply.returncode:
            result["complete"] = False
            continue
        try:
            rows = json.loads(reply.stdout)
            if isinstance(rows, dict):
                rows = next((rows[k] for k in ("users", "sessions", "user_list") if isinstance(rows.get(k), list)), None)
            if not isinstance(rows, list):
                raise ValueError
        except ValueError:
            result["complete"] = False
            continue
        result["available"] = True
        tag, users = str(config.get("inbound_tag", "")), config.get("users", {})
        result["configured_accounts"].update(("anyconnect", uint(uid), tag) for uid in users.values() if uint(uid))
        for row in rows:
            if not isinstance(row, dict):
                continue
            row = {str(k).lower().replace("-", "_").replace(" ", "_"): v for k, v in row.items()}
            name = row.get("username", row.get("user", row.get("name")))
            uid = uint(users.get(name))
            result["sessions"] += 1
            if not uid:
                result["unmapped_sessions"] += 1
                continue
            key = ("anyconnect", uid, tag)
            result["active"].add(key)
            rx = next((row[k] for k in ("raw_rx", "bytes_in", "rx_bytes", "rx") if k in row), 0)
            tx = next((row[k] for k in ("raw_tx", "bytes_out", "tx_bytes", "tx") if k in row), 0)
            identity = (tag, row.get("id"), row.get("full_session", row.get("session_id")), row.get("connected_at"))
            result["series"][identity] = (key, uint(rx) + uint(tx))
    return result


def live_xray(data_dir, inventory, port):
    result = {p: live_result("Xray StatsService user counters; stats presence does not prove loaded/online users")
              for p in PROTOCOLS if p not in NATIVE_FIELDS.values()}
    binary = os.environ.get("XRAY_EXECUTABLE_PATH", str(Path(data_dir) / "xray-core/xray"))
    # NodeAgent takes the actual runtime API_INBOUND port over its bootstrap
    # default. Query that same port unless the operator explicitly overrides it.
    port = port if port is not None else inventory.get("xray_api_port") or 10085
    reply = command([binary, "api", "statsquery", "-server=127.0.0.1:" + str(port),
                     "-pattern=user>>>", "-reset=false"], 5)
    if reply is None or reply.returncode:
        for value in result.values():
            value["unavailable_reason"] = "stats_command_unavailable" if reply is None else "stats_command_failed"
        return result
    # The pinned CLI can emit either JSON or protobuf text; do not infer zero
    # traffic from an unrecognized successful command response.
    rows = None
    try:
        decoded = json.loads(reply.stdout)
        if isinstance(decoded, dict) and isinstance(decoded.get("stat"), list):
            rows = [(row.get("name"), uint(row.get("value"))) for row in decoded["stat"]]
    except ValueError:
        pass
    if rows is None and ("stat:" in reply.stdout or not reply.stdout.strip()):
        rows = [(bytes(name, "utf-8").decode("unicode_escape"), uint(value))
                for name, value in re.findall(r'name:\s*"([^"\n]+)"\s+value:\s*(\d+)', reply.stdout)]
    if rows is None:
        for value in result.values():
            value["unavailable_reason"] = "unsupported_stats_response"
        return result
    labels = xray_identity_index(inventory)
    for value in result.values():
        value["available"] = True
    unmatched = 0
    for name, value in rows:
        if not isinstance(name, str):
            continue
        parts = name.split(">>>")
        key = labels.get(parts[1]) if len(parts) == 4 and parts[0] == "user" else None
        if key and key[0] in result:
            result[key[0]]["series"][name] = (key, value)
        else:
            unmatched += 1
    for value in result.values():
        value["unattributed_xray_family_series"] = unmatched
    return result


def live_inventory(data_dir, inventory, port):
    result = live_xray(data_dir, inventory, port)
    for protocol in ("wireguard", "amneziawg"):
        result[protocol] = live_wireguard(data_dir, inventory, protocol)
    result["openvpn"] = live_openvpn(data_dir, inventory)
    for protocol in ("l2tp", "pptp"):
        result[protocol] = live_ppp(data_dir, inventory, protocol)
    result["ikev2"] = live_ikev2(data_dir, inventory)
    result["anyconnect"] = live_anyconnect(data_dir, inventory)
    return result


def audit_snapshot(args):
    global COMMAND_DEADLINE
    start = utc_now()
    previous = COMMAND_DEADLINE
    COMMAND_DEADLINE = time.monotonic() + 45
    try:
        inventory = runtime_inventory(args.data_dir)
        return {"started_utc": start, "policy": inventory,
                "db": db_inventory(args, inventory),
                "accounting": accounting_inventory(args.data_dir, inventory),
                "live": live_inventory(args.data_dir, inventory, args.xray_api_port),
                "finished_utc": utc_now(),
                "endpoint_budget_exhausted": time.monotonic() >= COMMAND_DEADLINE}
    finally:
        COMMAND_DEADLINE = previous


def receipt_totals(db, epoch, inventory):
    totals = {}
    for uid, row in db["receipts"].items():
        watermark = decoded_json(row.get("ledger_json"))
        if watermark.get("version") != 2 or not epoch or watermark.get("epoch") != epoch:
            continue
        for label, total in (watermark.get("totals", {}) or {}).items():
            owner = parse_reflection_key(label)
            if owner:
                protocol = resolved_protocol(owner[0], owner[1], inventory)
                totals[(protocol, uid, owner[1])] = uint(total)
    return totals


def receipt_width_distribution(db):
    """Bucket stored reflection receipts by how many owners each one carries.

    Each receipt is stored per (node, user), so this measures the real decoded
    owner count per user rather than assuming one. It is read-only and reports
    aggregate counts plus the widest receipt size, never identities.
    """
    widths = []
    unreadable = 0
    for row in db["receipts"].values():
        watermark = decoded_json(row.get("ledger_json"))
        if not watermark:
            unreadable += 1
            continue
        widths.append(len(watermark.get("totals", {}) or {}))
    buckets = {"0": 0, "1-4": 0, "5-16": 0, "17-64": 0, "65-256": 0, "257-1024": 0, "1025+": 0}
    for width in widths:
        if width == 0:
            buckets["0"] += 1
        elif width <= 4:
            buckets["1-4"] += 1
        elif width <= 16:
            buckets["5-16"] += 1
        elif width <= 64:
            buckets["17-64"] += 1
        elif width <= 256:
            buckets["65-256"] += 1
        elif width <= 1024:
            buckets["257-1024"] += 1
        else:
            buckets["1025+"] += 1
    return {"receipts": len(widths), "unreadable": unreadable,
            "max_owners": max(widths) if widths else 0,
            "mean_owners": round(sum(widths) / len(widths), 2) if widths else 0.0,
            "buckets": buckets}


def reconciliation(before, after, elapsed):
    result = {}
    a0, a1, db = before["accounting"], after["accounting"], after["db"]
    reflected = receipt_totals(db, a1["epoch"], after["policy"])
    policy_changed = before["policy"]["rows"] != after["policy"]["rows"]
    for protocol in PROTOCOLS:
        live0, live1 = before["live"][protocol], after["live"][protocol]
        counter_elapsed = live1["observed_monotonic"] - live0["observed_monotonic"]
        expected = {k for k in db["expected"] if k[0] == protocol}
        policy_users = {k for k in after["policy"]["rows"] if k[0] == protocol}
        accounted = {k for k in a1["totals"] | a1["ledger"] if k[0] == protocol}
        native_delta, reset_series = {}, 0
        for label, (key, value) in live1["series"].items():
            previous = live0["series"].get(label)
            if previous is None or previous[0] != key or value < previous[1]:
                reset_series += 1
                continue
            native_delta[key] = native_delta.get(key, 0) + value - previous[1]
        logical_delta = {k: value - a0["totals"][k] for k, value in a1["totals"].items()
                         if k[0] == protocol and k in a0["totals"] and value >= a0["totals"][k]}
        traffic = {k for k, value in native_delta.items() if value > 0}
        increments = {k for k, value in logical_delta.items() if value > 0}
        loaded = live1["loaded"]
        uids = lambda values: {key[1] for key in values}
        assigned = {uid for uid in db["assigned"][protocol] if uid in db["users"]}
        disabled = {uid for uid in assigned if uid in db["users"] and not eligible(db["users"][uid])}
        ledger = {k: v for k, v in a1["ledger"].items() if k[0] == protocol}
        pending_reflection = {k: v - reflected.get(k, 0) for k, v in ledger.items() if v > reflected.get(k, 0)}
        receipt_available = (db["node_scoped"] and db["status"].get("node_wireguard_usage_reflection") == "ok"
                             and a1["epoch"] is not None)
        stale_receipt_users = {uid for uid, row in db["receipts"].items()
                              if decoded_json(row.get("ledger_json")).get("epoch") not in (None, a1["epoch"])}
        queued_users = {uint(row.get("user_id")) for row in db["queue"] if not row.get("processed_at")}
        zero_accounting = traffic - increments
        only_accounting = increments - traffic
        mismatched_bytes = {k for k in native_delta.keys() & logical_delta.keys() if native_delta[k] != logical_delta[k]}
        regressions = {k for k in a1["totals"].keys() & a0["totals"].keys()
                       if k[0] == protocol and a1["totals"][k] < a0["totals"][k]}
        vanished = {k for k in a0["totals"] if k[0] == protocol and k not in a1["totals"]}
        configured = live1["configured_accounts"]
        state_family = protocol if protocol in NATIVE_FIELDS.values() else "xray"
        logical_available = a0["state_status"].get(state_family) == "ok" and a1["state_status"].get(state_family) == "ok"
        native_available = live0["available"] and live1["available"]
        per_user_db_delta = sum(uint(db["users"].get(uid, {}).get("used_traffic"))
                                - uint(before["db"]["users"].get(uid, {}).get("used_traffic"))
                                for uid in uids(expected) if uid in before["db"]["users"])
        result[protocol] = {
            "configured_in_node_policy": protocol in after["policy"]["inbounds"].values(),
            "panel": {"assigned_users": len(assigned) if db["expectation_complete"] else None,
                      "eligible_users": len(assigned - disabled) if db["expectation_complete"] else None,
                      "disabled_expired_or_quota_exhausted_users": len(disabled) if db["expectation_complete"] else None,
                      "expected_on_observed_node_inbounds": len(uids(expected)) if db["expectation_complete"] else None,
                      "expectation_scope": "DB services/enabled hosts/proxy exclusions intersected with this node's durable inbound inventory",
                      "all_panel_inbounds_verified": False},
            "runtime": {"policy_snapshot_users": len(uids(policy_users)),
                        "helper_config_accounts": len(uids(configured)) if configured else None,
                        "daemon_loaded_users": len(uids(loaded)) if loaded is not None else None,
                        "loaded_inventory_complete": live1["complete"] if loaded is not None else False,
                        "active_users": len(uids(live1["active"])) if live1["available"] and protocol in NATIVE_FIELDS.values() else None,
                        "sessions_or_recent_handshake_peers": live1["sessions"] if live1["available"] and protocol in NATIVE_FIELDS.values() else None,
                        "source": live1["source"], "available": live1["available"],
                        "unavailable_reason": live1.get("unavailable_reason"),
                        "unmapped_sessions": live1["unmapped_sessions"], "unmapped_accounts": live1["unmapped_accounts"],
                        "unattributed_xray_family_series": live1.get("unattributed_xray_family_series"),
                        "stale_session_records": live1["stale_records"],
                        "missing_policy_users": len(uids(expected - policy_users)) if db["expectation_complete"] else None,
                        "extra_policy_users": len(uids(policy_users - expected)) if db["expectation_complete"] else None,
                        "missing_runtime_users": len(uids(expected - loaded)) if loaded is not None and live1["complete"] and db["expectation_complete"] else None,
                        "extra_runtime_users": len(uids(loaded - expected)) if loaded is not None and db["expectation_complete"] else None},
            "accounting": {"users_with_live_counter_series": len(uids({v[0] for v in live1["series"].values()})) if live1["available"] else None,
                           "users_with_traffic_during_window": len(uids(traffic)) if live0["available"] and live1["available"] else None,
                           "users_with_nodeagent_accounting": len(uids(accounted)) if a1["ledger_status"] == "ok" or a1["state_status"].get(protocol if protocol in NATIVE_FIELDS.values() else "xray") == "ok" else None,
                           "users_with_committed_reflection_receipt": len(uids({k for k in reflected if k[0] == protocol})) if receipt_available else None,
                           "assigned_users_with_other_epoch_receipt": len(assigned & stale_receipt_users) if receipt_available else None,
                           "assigned_users_in_pending_controller_queue": len(assigned & queued_users) if db["status"].get("node_usage_user_queue") == "ok" else None,
                           "users_awaiting_reflection": len(uids(pending_reflection)) if receipt_available else None,
                           "awaiting_reflection_raw_bytes": sum(pending_reflection.values()) if receipt_available else None,
                           "traffic_without_logical_increment_candidates": len(uids(zero_accounting)) if live0["available"] and live1["available"] else None,
                           "logical_increment_without_live_traffic_candidates": len(uids(only_accounting)) if live0["available"] and live1["available"] else None,
                           "counter_delta_mismatch_candidates": len(uids(mismatched_bytes)) if logical_available and native_available else None,
                           "counter_regression_users": len(uids(regressions)), "vanished_counter_users": len(uids(vanished)),
                           "stale_owner_candidates": len(uids(accounted - policy_users)),
                           "unreflected_stale_owner_candidates": len(uids(set(pending_reflection) - policy_users)) if receipt_available else None,
                           "reflection_ahead_of_ledger_owners": sum(1 for k, v in ledger.items() if reflected.get(k, 0) > v),
                           "native_counter_delta_bytes": sum(native_delta.values()) if live0["available"] and live1["available"] else None,
                           "nodeagent_logical_counter_delta_bytes": sum(logical_delta.values()) if logical_available else None,
                           "uncomparable_new_or_reset_native_series": reset_series,
                           "counter_observation_seconds": round(counter_elapsed, 3),
                           "native_bytes_per_second": sum(native_delta.values()) / counter_elapsed if counter_elapsed > 0 and live0["available"] and live1["available"] else None,
                           "db_usage_delta_assigned_users_all_protocols": per_user_db_delta if db["status"].get("users") == "ok" else None,
                           "db_protocol_usage_bytes": None,
                           "db_protocol_usage_reason": "users.used_traffic spans protocols/nodes and coefficients; raw receipt totals are compared separately"},
            "verdict": "INCOMPLETE_OBSERVABILITY",
            "notes": ["null means unobservable, not zero or PASS",
                      "differences are candidates: snapshots are not atomic; checkpoint/reflection lag, churn and resets require follow-up",
                      "persisted receipt validates reflection only for the same accounting epoch; raw ACK is never treated as reflection",
                      "retained legacy anchors and replay receipts are expected durable safety state, not proof of double billing"],
        }
        if policy_changed:
            result[protocol]["notes"].append("runtime policy changed during sampling; endpoint comparisons require review")
    return result


def utc_now():
    return datetime.now(timezone.utc).isoformat()


def command(argv, timeout=5):
    if COMMAND_DEADLINE is not None:
        remaining = COMMAND_DEADLINE - time.monotonic()
        if remaining <= 0:
            return None
        timeout = min(timeout, remaining)
    try:
        return subprocess.run(argv, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                              stderr=subprocess.DEVNULL, text=True, timeout=timeout,
                              check=False, env={**os.environ, "LC_ALL": "C"})
    except (OSError, subprocess.TimeoutExpired):
        return None


def read_proc(pid, name):
    try:
        return Path(f"/proc/{pid}/{name}").read_bytes()
    except OSError:
        return b""


def proc_stat(pid):
    raw = read_proc(pid, "stat").decode(errors="replace")
    if not raw:
        return None
    try:
        fields = raw.rsplit(")", 1)[1].split()
        # utime (14) and stime (15) are the user/kernel split; stime growth with
        # flat utime points at syscall/fsync cost rather than user-space loops.
        return {"ticks": int(fields[11]) + int(fields[12]), "start_ticks": int(fields[19]),
                "utime_ticks": int(fields[11]), "stime_ticks": int(fields[12])}
    except (IndexError, ValueError):
        return None


def proc_io_counters(pid):
    # Read-only syscall/IO accounting. syscw counts write(2) calls, which is the
    # cheapest way to confirm or refute repeated durable state rewrites without
    # tracing or attaching to the process.
    values = {}
    raw = read_proc(pid, "io").decode(errors="replace")
    for line in raw.splitlines():
        key, sep, value = line.partition(":")
        if sep:
            values[key.strip()] = value.strip()
    keys = ("rchar", "wchar", "syscr", "syscw", "read_bytes", "write_bytes")
    if not any(key in values for key in keys):
        return None
    out = {}
    for key in keys:
        try:
            out[key] = int(values.get(key, ""))
        except ValueError:
            if key in values:
                return None
    return out


def proc_thread_cpu(pid):
    # Per-thread CPU is the decisive read-only diagnostic when the aggregate
    # process CPU is high but no single request path explains it: it shows
    # whether the cost is spread across worker threads, concentrated in one
    # loop, or dominated by runtime GC threads. No thread names or stacks are
    # emitted, only thread id and CPU ticks.
    threads = []
    try:
        entries = list(Path(f"/proc/{pid}/task").iterdir())
    except OSError:
        return None
    for entry in entries:
        if not entry.name.isdigit():
            continue
        try:
            raw = (entry / "stat").read_text()
            fields = raw.rsplit(")", 1)[1].split()
            threads.append({"tid": int(entry.name),
                            "utime_ticks": int(fields[11]), "stime_ticks": int(fields[12])})
        except (OSError, IndexError, ValueError):
            continue
    return threads or None


def thread_cpu_delta(before, after, hz, top=8):
    if not before or not after:
        return None
    previous = {row["tid"]: row for row in before}
    deltas = []
    for row in after:
        old = previous.get(row["tid"])
        if not old:
            continue
        ticks = (row["utime_ticks"] - old["utime_ticks"]) + (row["stime_ticks"] - old["stime_ticks"])
        if ticks <= 0:
            continue
        deltas.append({"tid": row["tid"], "cpu_seconds": round(ticks / hz, 4),
                       "user_seconds": round((row["utime_ticks"] - old["utime_ticks"]) / hz, 4),
                       "system_seconds": round((row["stime_ticks"] - old["stime_ticks"]) / hz, 4)})
    deltas.sort(key=lambda row: row["cpu_seconds"], reverse=True)
    return deltas[:top]


def process_info(pid):
    status = read_proc(pid, "status").decode(errors="replace")
    values = {}
    for line in status.splitlines():
        key, sep, value = line.partition(":")
        if sep:
            values[key] = value.strip()
    stat = proc_stat(pid)
    if not stat:
        return None
    try:
        exe = os.readlink(f"/proc/{pid}/exe")
    except OSError:
        exe = ""
    return {"pid": pid, "exe": exe, "stat": stat,
            "rss_kib": int(values.get("VmRSS", "0 kB").split()[0]),
            "vsz_kib": int(values.get("VmSize", "0 kB").split()[0]),
            "threads": int(values.get("Threads", "0"))}


def node_lookup_hints(pid=0, limit=10):
    """Bounded, non-secret process-name hints used only when the node is not found."""
    hints = []
    try:
        entries = sorted((int(p.name), p) for p in Path("/proc").iterdir() if p.name.isdigit())
    except OSError:
        return hints
    for number, entry in entries:
        if pid and number != pid:
            continue
        try:
            comm = Path(f"/proc/{entry.name}/comm").read_text().strip()
        except OSError:
            continue
        if not re.fullmatch(r"[A-Za-z0-9._-]{1,32}", comm):
            continue
        if "antimage" in comm.lower() or "node" in comm.lower():
            hints.append({"pid": number, "comm": comm})
            if len(hints) >= limit:
                break
    return hints


def find_node(pid=0):
    candidates = []
    try:
        entries = list(Path("/proc").iterdir())
    except OSError as exc:
        raise CollectorStageError("find_node", "cannot read /proc; rerun with root or read access to /proc",
                                  diagnostics={"reason": safe_message(exc)}) from exc
    for entry in entries:
        if not entry.name.isdigit():
            continue
        if pid and int(entry.name) != pid:
            continue
        info = process_info(int(entry.name))
        if not info:
            continue
        exe = Path(info["exe"]).name.lower().removesuffix(" (deleted)")
        if exe in ("antimage-node", "antimage-node.exe"):
            candidates.append(info)
    if len(candidates) > 1:
        raise CollectorStageError(
            "find_node", "multiple antimage-node processes; select the intended PID with --pid",
            diagnostics={"matching_pids": [c["pid"] for c in candidates]},
            hint="pass --pid <PID> for the node that serves the panel")
    return candidates[0] if candidates else None


def cpu_ticks():
    try:
        first = Path("/proc/stat").read_text().splitlines()[0].split()[1:]
        values = list(map(int, first))
        idle = values[3] + (values[4] if len(values) > 4 else 0)
        return sum(values[:8]), idle  # guest/guest_nice are already included in user/nice
    except (OSError, ValueError, IndexError):
        return None


def total_ram_bytes():
    try:
        for line in Path("/proc/meminfo").read_text().splitlines():
            if line.startswith("MemTotal:"):
                return int(line.split()[1]) * 1024
    except (OSError, ValueError, IndexError):
        pass
    return None


def collect_system_cpu():
    try:
        lines = Path("/proc/cpuinfo").read_text().splitlines()
        models = sorted({line.split(":", 1)[1].strip() for line in lines
                         if line.lower().startswith("model name")})
        online = Path("/sys/devices/system/cpu/online").read_text().strip()
        return {"model": models[0] if models else "unavailable",
                "logical_cpu_count": os.cpu_count(), "online_cpu_list": online}
    except OSError:
        return {"model": "unavailable", "logical_cpu_count": os.cpu_count()}


def disk_usage(path):
    total = rx = tx = 0
    try:
        for line in Path(path).read_text().splitlines()[2:]:
            fields = line.replace(":", " ").split()
            if len(fields) < 10:
                continue
            rx += int(fields[1])
            tx += int(fields[9])
            total += 1
        return {"interfaces": total, "rx_bytes": rx, "tx_bytes": tx}
    except (OSError, ValueError, IndexError):
        return None




def binary_info(path):
    if not path:
        return {"available": False}
    try:
        digest = hashlib.sha256()
        with open(path, "rb") as stream:
            for block in iter(lambda: stream.read(1 << 20), b""):
                digest.update(block)
        # antimage-node has no version-only CLI: executing it with -version
        # starts the service. Inspect build metadata without running the binary.
        metadata = {}
        if shutil.which("go"):
            result = command(["go", "version", "-m", path], timeout=5)
            if result and not result.returncode:
                revision = re.search(r"vcs.revision=([a-f0-9]{40,64})\b", result.stdout)
                go_version = re.search(r"\b(go1\.\d+(?:\.\d+)?)\b", result.stdout)
                if revision:
                    metadata["vcs_revision"] = revision.group(1)
                if go_version:
                    metadata["go_version"] = go_version.group(1)
        return {"available": True, "sha256": digest.hexdigest(), "build_metadata": metadata,
                "note": "binary is never executed for version discovery; SHA identifies it when Go metadata is unavailable"}
    except OSError:
        return {"available": False}


def xray_version(path):
    if Path(path).name not in ("xray", "xray.exe"):
        return None
    result = command([path, "version"], 3)
    match = re.search(r"^Xray\s+([0-9]+(?:\.[0-9]+){1,3})\b", result.stdout) if result and not result.returncode else None
    return match.group(1) if match else None


def process_limits(pid):
    result = {"affinity_logical_cpus": None, "cgroup_quota_cpus": None, "cgroup_memory_max_bytes": None}
    try:
        result["affinity_logical_cpus"] = len(os.sched_getaffinity(pid))
    except (AttributeError, OSError):
        pass
    cgroup = read_proc(pid, "cgroup").decode(errors="replace")
    for row in cgroup.splitlines():
        if not row.startswith("0::"):
            continue
        root = Path("/sys/fs/cgroup")
        relative = row[3:].lstrip("/")
        if ".." in Path(relative).parts:
            continue
        path = root / relative
        value = read_text_limited(path / "cpu.max")
        if value:
            parts = value.split()
            if len(parts) == 2 and parts[0].isdigit() and uint(parts[1]):
                result["cgroup_quota_cpus"] = uint(parts[0]) / uint(parts[1])
        value = read_text_limited(path / "memory.max")
        if value and value.strip().isdigit():
            result["cgroup_memory_max_bytes"] = uint(value.strip())
    return result


def interval_configuration(pid):
    allowed = {"ANTIMAGE_NODE_QUOTA_ENFORCEMENT_INTERVAL", "ANTIMAGE_NODE_ACCOUNTING_CHECKPOINT_INTERVAL"}
    values = {}
    for entry in read_proc(pid, "environ").split(b"\x00"):
        key, sep, value = entry.partition(b"=")
        name = key.decode(errors="replace")
        if sep and name in allowed:
            text = value.decode(errors="replace")
            if re.fullmatch(r"[0-9]+(?:\.[0-9]+)?(?:ms|s|m|h)?", text):
                values[name] = text
    return {"running_process_interval_overrides": values,
            "note": "unset values use the binary's defaults; defaults are not guessed from a possibly different checkout"}


def sample(args):
    started = utc_now()
    node = find_node(args.pid)
    if not node:
        raise CollectorStageError(
            "find_node", "antimage-node process not found; pass --pid <PID> if it runs under another name",
            diagnostics={"requested_pid": args.pid or None,
                         "process_name_hints": node_lookup_hints(args.pid)},
            hint="confirm the node binary path with: systemctl show -p ExecStart antimage-node")
    # Heavy read-only endpoint queries run at the two boundaries, never on
    # every CPU sample. Their source timestamps are retained in the report.
    audit_before = audit_snapshot(args)
    try:
        namespace_matches = os.readlink("/proc/self/ns/net") == os.readlink("/proc/" + str(node["pid"]) + "/ns/net")
    except OSError:
        namespace_matches = False
    if not namespace_matches:
        for value in audit_before["live"].values():
            value["available"], value["complete"], value["loaded"] = False, False, None
    hz = os.sysconf("SC_CLK_TCK")
    logical = os.cpu_count() or 1
    cpu_before = cpu_ticks()
    net_before = disk_usage("/proc/net/dev")
    proc_before = proc_stat(node["pid"])
    io_before = proc_io_counters(node["pid"])
    threads_before = proc_thread_cpu(node["pid"])
    memory = total_ram_bytes()
    start_mono = time.monotonic()
    duration = max(60, args.sample_seconds)
    samples = []
    previous_sample = None
    previous_files = {}
    metadata_changes = {p: 0 for p in ("xray",) + tuple(NATIVE_FIELDS.values())}
    while time.monotonic() - start_mono < duration:
        current = process_info(node["pid"])
        if not current or current["stat"]["start_ticks"] != node["stat"]["start_ticks"]:
            raise CollectorStageError(
                "sampling", "antimage-node process identity changed during read-only sampling (restart or PID reuse)",
                diagnostics={"pid": node["pid"]})
        elapsed_now = time.monotonic() - start_mono
        changed = []
        for protocol in metadata_changes:
            try:
                stamp = (Path(args.data_dir) / protocol / "usage-state.json").stat().st_mtime_ns
                if protocol in previous_files and previous_files[protocol] != stamp:
                    changed.append(protocol)
                    metadata_changes[protocol] += 1
                previous_files[protocol] = stamp
            except OSError:
                pass
        interval_cpu = None
        if previous_sample:
            dt = elapsed_now - previous_sample[0]
            interval_cpu = 100 * (current["stat"]["ticks"] - previous_sample[1]) / hz / dt if dt > 0 else None
        samples.append({"elapsed_seconds": round(elapsed_now, 3),
                        "cpu_ticks": current["stat"]["ticks"], "rss_kib": current["rss_kib"],
                        "vsz_kib": current["vsz_kib"], "threads": current["threads"],
                        "utime_ticks": current["stat"].get("utime_ticks"),
                        "stime_ticks": current["stat"].get("stime_ticks"),
                        "cpu_percent_one_core_since_previous": interval_cpu,
                        "accounting_state_changed_protocols": changed})
        previous_sample = (elapsed_now, current["stat"]["ticks"])
        time.sleep(min(5, max(.5, args.sample_interval)))
    end_mono = time.monotonic()
    proc_after = proc_stat(node["pid"])
    io_after = proc_io_counters(node["pid"])
    threads_after = proc_thread_cpu(node["pid"])
    cpu_after = cpu_ticks()
    net_after = disk_usage("/proc/net/dev")
    elapsed = end_mono - start_mono
    process_cpu_seconds = ((proc_after["ticks"] - proc_before["ticks"]) / hz) if proc_after and proc_before else 0
    normalized_cpu = 100 * process_cpu_seconds / elapsed / logical
    one_core_cpu = 100 * process_cpu_seconds / elapsed
    cpu_split = None
    # Older or stubbed readers may not carry the user/system split; the split is
    # additional diagnostic evidence and must never fail the whole collection.
    if proc_after and proc_before and "utime_ticks" in proc_after and "utime_ticks" in proc_before:
        utime_seconds = (proc_after["utime_ticks"] - proc_before["utime_ticks"]) / hz
        stime_seconds = (proc_after["stime_ticks"] - proc_before["stime_ticks"]) / hz
        cpu_split = {"user_seconds": round(utime_seconds, 3), "system_seconds": round(stime_seconds, 3),
                     "user_percent_one_core": round(100 * utime_seconds / elapsed, 3),
                     "system_percent_one_core": round(100 * stime_seconds / elapsed, 3)}
    io_activity = None
    if io_after and io_before:
        io_activity = {key: io_after[key] - io_before[key] for key in io_after}
        io_activity["write_calls_per_second"] = round(io_activity.get("syscw", 0) / elapsed, 3) if elapsed > 0 else None
        io_activity["read_calls_per_second"] = round(io_activity.get("syscr", 0) / elapsed, 3) if elapsed > 0 else None
    thread_cpu = thread_cpu_delta(threads_before, threads_after, hz)
    if cpu_before and cpu_after:
        total_delta = cpu_after[0] - cpu_before[0]
        idle_delta = cpu_after[1] - cpu_before[1]
        host_cpu_percent = 100 * (total_delta - idle_delta) / total_delta if total_delta > 0 else None
    else:
        host_cpu_percent = None
    process_current = process_info(node["pid"])
    if not process_current:
        raise CollectorStageError("sampling", "antimage-node exited before collection completed",
                                  diagnostics={"pid": node["pid"], "observed_seconds": round(elapsed, 3)})
    audit_after = audit_snapshot(args)
    if not namespace_matches:
        for value in audit_after["live"].values():
            value["available"], value["complete"], value["loaded"] = False, False, None
    data_dir = args.data_dir
    xray_path = os.environ.get("XRAY_EXECUTABLE_PATH", str(Path(data_dir) / "xray-core/xray"))
    xray_proc = next((process_info(int(p.name)) for p in Path("/proc").iterdir() if p.name.isdigit()
                      and (process_info(int(p.name)) or {}).get("exe", "").endswith("/xray")), None)
    end_net = net_after or {"rx_bytes": 0, "tx_bytes": 0}
    start_net = net_before or {"rx_bytes": 0, "tx_bytes": 0}
    traffic = {"aggregate_rx_bytes_per_second": max(0, end_net["rx_bytes"] - start_net["rx_bytes"]) / elapsed,
               "aggregate_tx_bytes_per_second": max(0, end_net["tx_bytes"] - start_net["tx_bytes"]) / elapsed,
               "scope": "host network interfaces; includes non-VPN traffic"}
    report = {
        "schema_version": 2, "sample_started_utc": started, "sample_finished_utc": utc_now(),
        "observation_seconds": round(elapsed, 3),
        "host": {"cpu": collect_system_cpu(), "total_ram_bytes": memory,
                 "node_process_limits": process_limits(node["pid"])},
        "node_process": {"pid": node["pid"], "binary": binary_info("/proc/" + str(node["pid"]) + "/exe"),
                         "cpu_percent_one_core": round(one_core_cpu, 3),
                         "cpu_percent_normalized_by_logical_cpus": round(normalized_cpu, 3),
                         "cpu_split": cpu_split,
                         "io_activity": io_activity,
                         "top_threads_by_cpu": thread_cpu,
                         "host_total_cpu_percent": host_cpu_percent,
                         "rss_bytes_start": samples[0]["rss_kib"] * 1024,
                         "rss_bytes_end": process_current["rss_kib"] * 1024,
                         "rss_bytes_max": max(row["rss_kib"] for row in samples) * 1024,
                         "vsz_bytes_end": process_current["vsz_kib"] * 1024,
                         "threads_end": process_current["threads"], "samples": samples},
        "database": {"source_status": audit_after["db"]["status"],
                     "discovery": getattr(args, "database_discovery", "explicit"),
                     "discovery_note": getattr(args, "database_note", None),
                     "node_scoped_accounting": audit_after["db"]["node_scoped"],
                     "node_identity_mismatch": audit_after["db"]["node_identity_mismatch"],
                     "collector_select_calls": audit_before["db"]["query_count"] + audit_after["db"]["query_count"],
                     "configured_users_total": sum(str(u.get("status", "")).lower() != "deleted" for u in audit_after["db"]["users"].values()) if audit_after["db"]["status"].get("users") == "ok" else None,
                     "eligible_users_total": sum(eligible(u) for u in audit_after["db"]["users"].values()) if audit_after["db"]["status"].get("users") == "ok" else None},
        "runtime_policy": {"status": audit_after["policy"]["status"],
                           "revision": audit_after["policy"]["revision"],
                           "protocol_mix": {p: list(audit_after["policy"]["inbounds"].values()).count(p) for p in PROTOCOLS},
                           "node_policy_is_not_live_daemon_proof": True},
        "protocol_reconciliation": reconciliation(audit_before, audit_after, elapsed),
        "audit_boundaries": {"before_started": audit_before["started_utc"], "before_finished": audit_before["finished_utc"],
                             "after_started": audit_after["started_utc"], "after_finished": audit_after["finished_utc"],
                             "before_endpoint_budget_exhausted": audit_before.get("endpoint_budget_exhausted", False),
                             "after_endpoint_budget_exhausted": audit_after.get("endpoint_budget_exhausted", False),
                             "note": "sources are sequential read-only snapshots, not a distributed atomic snapshot"},
        "durability_audit": {k: audit_after["accounting"][k] for k in ("ledger_status", "anchor_status", "anchor_bytes", "legacy_deliveries", "duplicate_owners", "duplicate_children", "invalid_counters", "state_bytes", "state_status")},
        "reflection_ledger": {
            "on_disk_version": audit_after["accounting"]["ledger_version"],
            "receipt_width": receipt_width_distribution(audit_after["db"]),
            "note": "on_disk_version selects the live NodeAgent reflection path; receipt_width is the real decoded owner count per stored user receipt"},
        "traffic": traffic,
        "configuration": interval_configuration(node["pid"]),
        "collector_net_namespace_matches_node": namespace_matches,
        "runtime_instrumentation": {"goroutine_count": None, "accounting_calls": None,
                                    "reflection_calls": None, "quota_tick_calls": None,
                                    "accounting_state_change_observations": metadata_changes,
                                    "heap_bytes": None, "gc_cycles": None, "gc_cpu_seconds": None,
                                    "internal_db_query_calls": None,
                                    "reason": "current production NodeAgent does not expose these aggregate counters; state-file changes are activity observations, not tick/call counts",
                                    "cpu_hotspot_attribution": "unavailable without existing process profiling instrumentation; timestamped state-change correlation is not causal attribution"},
        "xray": {"binary": binary_info("/proc/" + str(xray_proc["pid"]) + "/exe" if xray_proc else xray_path),
                 "pid": xray_proc["pid"] if xray_proc else None,
                 "version": xray_version(xray_path)},
        "confidence_notes": ["process CPU is for the antimage-node PID only; child daemons are excluded",
                             "aggregate interface traffic includes unrelated host traffic",
                             "inbound inventory is the node durable policy; a wholly missing panel inbound requires Controller-side desired-config evidence",
                             "WireGuard recent handshake is a heuristic, not proof of application traffic",
                             "raw ACK, pending/queued delivery and staged watermark do not prove committed billing",
                             "restart safety and historical exact-once behavior cannot be proven from two read-only snapshots alone",
                             "INCOMPLETE_OBSERVABILITY means some requested evidence is unavailable and must not be interpreted as PASS"],
    }
    print(json.dumps(report, indent=2, sort_keys=True))
    return report


def main(argv=None):
    install_signal_handlers()
    parser = CollectorArgumentParser(description=__doc__)
    group = parser.add_mutually_exclusive_group()
    group.add_argument("--sqlite", help="SQLite DB file; opened with mode=ro")
    group.add_argument("--mysql-defaults", help="existing MySQL client defaults file, must be mode 0600")
    parser.add_argument("--database", default="antimage", help="database name when using --mysql-defaults")
    parser.add_argument("--node-id", type=int, default=0, help="panel node ID, used only to scope read-only accounting SELECTs")
    parser.add_argument("--pid", type=int, default=0, help="select one antimage-node process if multiple are running")
    parser.add_argument("--data-dir", default=os.environ.get("ANTIMAGE_NODE_DATA_DIR", "/var/lib/antimage-node"))
    parser.add_argument("--xray-api-port", type=int, default=None,
                        help="override local StatsService port; otherwise use runtime API_INBOUND, then 10085")
    parser.add_argument("--sample-seconds", type=int, default=60)
    parser.add_argument("--sample-interval", type=float, default=2)
    parser.add_argument("--output", default=None,
                        help="also write the JSON document to this path so a dropped SSH session cannot lose it")
    args = None
    try:
        args = parser.parse_args(argv)
        if not 60 <= args.sample_seconds <= 900 or not .5 <= args.sample_interval <= 10:
            raise CollectorStageError("arguments", "sampling requires 60..900 seconds and an interval of 0.5..10 seconds")
        if args.pid < 0 or args.node_id < 0 or (args.xray_api_port is not None and not 0 < args.xray_api_port <= 65535):
            raise CollectorStageError("arguments", "invalid node ID or local Xray API port")
        if sys.platform != "linux":
            raise CollectorStageError("arguments", "Linux /proc collector only")
        if args.sqlite or args.mysql_defaults:
            args.database_discovery = "explicit"
            args.database_note = None
        else:
            args.database_discovery, args.database_note = discover_database(args)
    except SystemExit as exc:
        # argparse --help prints its own text and exits before any collection work.
        return 0 if exc.code in (0, None) else int(exc.code or 1)
    except CollectorStageError as exc:
        payload, code = error_payload(exc), 2
    else:
        try:
            report = sample(args)
        except BrokenPipeError:
            # The reader closed the pipe; the report JSON had nowhere to go.
            print("collector: stdout closed before the report was written", file=sys.stderr)
            return 0
        except CollectorStageError as exc:
            payload, code = error_payload(exc), 1
        except BaseException as exc:  # noqa: BLE001 - no exit path may leave stdout empty
            # SystemExit and KeyboardInterrupt are BaseExceptions: a bare SystemExit
            # raised inside sample() used to escape the old handler and produce a 0-byte report.
            stage = getattr(exc, "stage", None) or "sampling"
            payload = error_payload(CollectorStageError(stage, exc))
            payload["exception_type"] = type(exc).__name__
            code = 1
        else:
            # sample() already emitted the single report document on stdout; mirror it
            # to --output so an SSH disconnect cannot leave a 0-byte artifact.
            output_path = getattr(args, "output", None)
            if output_path:
                try:
                    write_json_file(output_path, json.dumps(report, indent=2, sort_keys=True))
                except OSError as exc:
                    print(f"collector: cannot write {output_path}: {safe_message(exc)}", file=sys.stderr)
                    output_path = None
            print(f"status=ok stage=report report={output_path or 'stdout'} "
                  f"observation_seconds={report.get('observation_seconds')}", file=sys.stderr)
            sys.stderr.flush()
            return 0
    # Never print raw exceptions: they can carry database rows, usernames, keys or daemon replies.
    text = json.dumps(payload, indent=2, sort_keys=True)
    output_path = getattr(args, "output", None)
    if output_path:
        try:
            write_json_file(output_path, text)
        except OSError as exc:
            print(f"collector: cannot write {output_path}: {safe_message(exc)}", file=sys.stderr)
            output_path = None
    print(text)
    sys.stdout.flush()
    print(f"status={payload['status']} stage={payload['stage']} report={output_path or 'stdout'} "
          f"observation_seconds={payload.get('observation_seconds')}", file=sys.stderr)
    sys.stderr.flush()
    return code


if __name__ == "__main__":
    sys.exit(main())
