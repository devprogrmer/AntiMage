"""Regression fixtures for read-only reconciliation and identity redaction."""
import argparse
import copy
import contextlib
import hashlib
import io
import itertools
import json
from pathlib import Path
import sqlite3
import subprocess
import tempfile
import unittest
from unittest import mock

import production_baseline as audit


class CollectorTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.args = argparse.Namespace(sqlite=str(self.root / "panel.db"), mysql_defaults=None,
            database="antimage", node_id=7, data_dir=str(self.root), xray_api_port=10085)
        self.secrets = ["SECRET-username", "SECRET-password", "SECRET-token", "SECRET-public-key", "SECRET-uuid"]
        users = [{"user_id": 871234567, "username": self.secrets[0], "vpn_username": self.secrets[0],
                  "password": self.secrets[1], "public_key": self.secrets[3], "email": "SECRET-uuid.tag",
                  "status": "active", "reflected_usage_batch_id": "raw-ACK"},
                 {"user_id": 871234568, "username": "OTHER-secret", "status": "disabled"}]
        native = {"session_callback": {"node_id": 7, "token": self.secrets[2]}}
        self.tags = {}
        inbounds = []
        for protocol in audit.PROTOCOLS:
            tag = "secret-tag-" + protocol
            self.tags[protocol] = tag
            if protocol in audit.NATIVE_FIELDS.values():
                field = next(k for k, p in audit.NATIVE_FIELDS.items() if p == protocol)
                native[field] = [{"tag": tag, "users": users, "peers": users}]
            else:
                inbounds.append({"protocol": protocol, "tag": tag})
                native.setdefault("xray_policies", []).append({**users[0], "email": "SECRET-uuid." + protocol,
                                                               "protocol": protocol, "inbound_tag": tag})
        self.write("accounting/runtime-policy.json", {"revision": 12,
            "config_json": json.dumps({"inbounds": inbounds}), "native_json": json.dumps(native)})
        db = sqlite3.connect(self.args.sqlite)
        db.executescript("""
            CREATE TABLE users(id INTEGER PRIMARY KEY,status TEXT,used_traffic INTEGER,data_limit INTEGER,
                expire INTEGER,service_id INTEGER,admin_disabled_at TEXT,service_limit_disabled_at TEXT);
            INSERT INTO users VALUES(871234567,'active',100,10000,0,1,NULL,NULL);
            INSERT INTO users VALUES(871234568,'disabled',10,0,0,1,NULL,NULL);
            CREATE TABLE proxies(id INTEGER,user_id INTEGER,type TEXT);
            CREATE TABLE hosts(id INTEGER,inbound_tag TEXT,is_disabled INTEGER);
            CREATE TABLE service_hosts(service_id INTEGER,host_id INTEGER);
            CREATE TABLE exclude_inbounds_association(proxy_id INTEGER,inbound_tag TEXT);
            CREATE TABLE inbounds(id INTEGER,tag TEXT);
            CREATE TABLE node_wireguard_usage_reflection(node_id INTEGER,user_id INTEGER,batch_id TEXT,ledger_json TEXT);
            CREATE TABLE node_usage_user_queue(node_id INTEGER,user_id INTEGER,used_traffic INTEGER,processed_at TEXT);
        """)
        for n, protocol in enumerate(audit.PROTOCOLS, 1):
            db.execute("INSERT INTO proxies VALUES(?,?,?)", (n, 871234567, protocol))
            db.execute("INSERT INTO hosts VALUES(?,?,0)", (n, self.tags[protocol]))
            db.execute("INSERT INTO service_hosts VALUES(1,?)", (n,))
            db.execute("INSERT INTO inbounds VALUES(?,?)", (n, self.tags[protocol]))
        db.commit()
        db.close()

    def write(self, name, obj):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(obj), encoding="utf-8")

    def snapshot(self):
        inventory = audit.runtime_inventory(self.root)
        live = {p: audit.live_result("test live read-only source") for p in audit.PROTOCOLS}
        return {"policy": inventory, "db": audit.db_inventory(self.args, inventory),
                "accounting": audit.accounting_inventory(self.root, inventory), "live": live}

    def test_every_protocol_native_service_assignments_and_proxy_exclusions(self):
        inventory = audit.runtime_inventory(self.root)
        actual = audit.db_inventory(self.args, inventory)
        self.assertTrue(actual["expectation_complete"])
        self.assertEqual({p for p, _, _ in actual["expected"]}, set(audit.PROTOCOLS))
        self.assertTrue(all(uid == 871234567 for _, uid, _ in actual["expected"]))
        db = sqlite3.connect(self.args.sqlite)
        db.execute("INSERT INTO exclude_inbounds_association VALUES(?,?)", (audit.PROTOCOLS.index("vless") + 1, self.tags["vless"]))
        db.commit()
        db.close()
        actual = audit.db_inventory(self.args, inventory)
        self.assertNotIn(("vless", 871234567, self.tags["vless"]), actual["expected"])
        self.assertIn(("ikev2", 871234567, self.tags["ikev2"]), actual["expected"])

    def test_database_read_only_and_byte_identical(self):
        before = Path(self.args.sqlite).read_bytes()
        source = audit.ReadOnlyDB(self.args)
        with self.assertRaises(sqlite3.OperationalError):
            source.db.execute("UPDATE users SET used_traffic=0")
        source.close()
        self.snapshot()
        self.assertEqual(before, Path(self.args.sqlite).read_bytes())

    def test_missing_schema_does_not_report_zero_missing_or_pass(self):
        db = sqlite3.connect(self.args.sqlite)
        db.execute("DROP TABLE service_hosts")
        db.close()
        snap = self.snapshot()
        report = audit.reconciliation(snap, snap, 60)
        for row in report.values():
            self.assertIsNone(row["panel"]["expected_on_observed_node_inbounds"])
            self.assertIsNone(row["runtime"]["missing_runtime_users"])
            self.assertEqual(row["verdict"], "INCOMPLETE_OBSERVABILITY")

    def test_node_identity_mismatch_prevents_wrong_node_receipt_comparison(self):
        self.args.node_id = 8
        snap = self.snapshot()
        self.assertTrue(snap["db"]["node_identity_mismatch"])
        report = audit.reconciliation(snap, snap, 60)
        self.assertIsNone(report["openvpn"]["accounting"]["users_with_committed_reflection_receipt"])

    def test_pascalcase_pending_not_double_summed_with_directional_counters(self):
        self.write("openvpn/usage-state.json", {"baseline": {
            "\u0000offline-total\u0000871234567\u0000" + self.tags["openvpn"]: 100},
            "pending": {"BatchID": "SECRET-batch", "Samples": [{"UserID": 871234567,
                "InboundTag": self.tags["openvpn"], "Value": 70, "Upload": 40, "Download": 30}]}})
        snap = self.snapshot()
        key = ("openvpn", 871234567, self.tags["openvpn"])
        self.assertEqual(snap["accounting"]["totals"][key], 100)
        self.assertEqual(snap["accounting"]["pending"][key][1], 70)

    def test_v2_ledger_and_hashed_sidecar_no_double_count(self):
        owner = {"protocol": "openvpn", "user_id": 871234567, "inbound_tag": self.tags["openvpn"],
                 "total": 140, "legacy_total": 80, "last_raw": 60}
        anchors = {"version": 1, "deliveries": [{"batch_id": "SECRET-root", "rows": [
            {**owner, "raw": 80, "child_id": "SECRET-child"}]}]}
        raw = json.dumps(anchors).encode()
        digest = hashlib.sha256(raw).hexdigest()
        self.write("accounting/deliveries-v1-" + digest + ".json", anchors)
        self.write("accounting/deliveries.json", {"version": 2, "legacy_anchors_hash": digest,
            "ledger": {"epoch": "SECRET-epoch", "owners": [owner], "latest": {}}, "deliveries": []})
        snap = self.snapshot()
        self.assertEqual(snap["accounting"]["anchor_status"], "ok")
        self.assertEqual(sum(snap["accounting"]["ledger"].values()), 140)
        self.assertEqual(snap["accounting"]["invalid_counters"], 0)
        self.assertEqual(snap["accounting"]["legacy_deliveries"], 1)

    def test_ledger_version_reports_on_disk_layout(self):
        # v1 keeps inline deliveries; v2 carries the cumulative owner ledger.
        # The reader must expose which one is on disk to pick the live path.
        self.write("accounting/deliveries.json", {"version": 1, "deliveries": []})
        self.assertEqual(self.snapshot()["accounting"]["ledger_version"], 1)
        self.write("accounting/deliveries.json", {"version": 2, "ledger": {"epoch": "e", "owners": [], "latest": {}}, "deliveries": []})
        self.assertEqual(self.snapshot()["accounting"]["ledger_version"], 2)

    def test_receipt_width_distribution_counts_real_owners(self):
        # A receipt with three owners must land in the 1-4 bucket, and the
        # widest receipt must be reported so a wide-owner deployment is visible.
        narrow = json.dumps({"version": 2, "epoch": "e", "totals": {
            json.dumps("xray") + ":" + json.dumps("a"): 1,
            json.dumps("xray") + ":" + json.dumps("b"): 2}})
        wide = json.dumps({"version": 2, "epoch": "e", "totals": {
            json.dumps("xray") + ":" + json.dumps("tag-%02d" % i): i for i in range(5)}})
        db = sqlite3.connect(self.args.sqlite)
        db.execute("INSERT INTO node_wireguard_usage_reflection VALUES(7,871234567,'ACK',?)", (narrow,))
        db.execute("INSERT INTO node_wireguard_usage_reflection VALUES(7,871234568,'ACK',?)", (wide,))
        db.commit()
        db.close()
        db_view = audit.db_inventory(self.args, audit.runtime_inventory(self.root))
        distribution = audit.receipt_width_distribution(db_view)
        self.assertEqual(distribution["receipts"], 2)
        self.assertEqual(distribution["max_owners"], 5)
        self.assertEqual(distribution["buckets"]["1-4"], 1)
        self.assertEqual(distribution["buckets"]["5-16"], 1)
        self.assertEqual(distribution["unreadable"], 0)

    def test_xray_family_ledger_and_reflection_resolve_to_actual_inbound_protocol(self):
        tag = self.tags["vmess"]
        self.write("accounting/deliveries.json", {"version": 2, "ledger": {"epoch": "e1", "owners": [
            {"protocol": "xray", "user_id": 871234567, "inbound_tag": tag, "total": 140,
             "legacy_total": 0, "last_raw": 60}]}})
        watermark = json.dumps({"version": 2, "epoch": "e1", "totals": {
            json.dumps("xray") + ":" + json.dumps(tag): 80}})
        db = sqlite3.connect(self.args.sqlite)
        db.execute("INSERT INTO node_wireguard_usage_reflection VALUES(7,871234567,'ACK',?)", (watermark,))
        db.commit()
        db.close()
        snap = self.snapshot()
        report = audit.reconciliation(snap, snap, 60)
        self.assertEqual(report["vmess"]["accounting"]["awaiting_reflection_raw_bytes"], 60)
        self.assertEqual(report["vmess"]["accounting"]["users_with_nodeagent_accounting"], 1)
        self.assertEqual(report["vmess"]["accounting"]["users_with_committed_reflection_receipt"], 1)
        self.assertEqual(report["xray"]["accounting"]["awaiting_reflection_raw_bytes"], 0)

    def test_ppp_zombie_reused_interface_and_finalized_records_not_active(self):
        protocol = "pptp"
        tag = self.tags[protocol]
        root = protocol + "/root"
        self.write(root + "/usage-helper.json", {"inbound_tag": tag, "users": {"IP": 871234567}})
        record = {"ID": "session", "UserID": 871234567, "Process": "boot:123:456",
                  "Interface": "ppp1", "Identity": "boot:77", "InboundTag": tag, "Final": False}
        self.write(root + "/ppp-accounting/active/session.json", record)
        def text(path):
            return {"/proc/sys/kernel/random/boot_id": "boot", "/sys/class/net/ppp1/ifindex": "77",
                    "/sys/class/net/ppp1/statistics/rx_bytes": "100", "/sys/class/net/ppp1/statistics/tx_bytes": "200"}.get(str(path).replace("\\", "/"))
        def proc(state):
            return ("123 (pppd) " + " ".join([state] + ["0"] * 18 + ["456"])).encode()
        inventory = audit.runtime_inventory(self.root)
        with mock.patch.object(audit, "read_text_limited", side_effect=text), mock.patch.object(audit, "read_proc", return_value=proc("S")):
            self.assertEqual(audit.live_ppp(self.root, inventory, protocol)["sessions"], 1)
        with mock.patch.object(audit, "read_text_limited", side_effect=text), mock.patch.object(audit, "read_proc", return_value=proc("Z")):
            self.assertEqual(audit.live_ppp(self.root, inventory, protocol)["sessions"], 0)
        record["Identity"] = "boot:old-interface"
        self.write(root + "/ppp-accounting/active/session.json", record)
        with mock.patch.object(audit, "read_text_limited", side_effect=text), mock.patch.object(audit, "read_proc", return_value=proc("S")):
            self.assertEqual(audit.live_ppp(self.root, inventory, protocol)["sessions"], 0)
        record["Identity"] = "boot:77"
        self.write(root + "/ppp-accounting/active/session.json", record)
        self.write(root + "/ppp-accounting/final/session.json", {**record, "Final": True})
        with mock.patch.object(audit, "read_text_limited", side_effect=text), mock.patch.object(audit, "read_proc", return_value=proc("S")):
            self.assertEqual(audit.live_ppp(self.root, inventory, protocol)["sessions"], 0)

    def test_reflection_requires_committed_node_and_epoch_receipt_raw_ack_insufficient(self):
        tag = self.tags["openvpn"]
        self.write("accounting/deliveries.json", {"version": 2, "ledger": {"epoch": "e1", "owners": [
            {"protocol": "openvpn", "user_id": 871234567, "inbound_tag": tag, "total": 140,
             "legacy_total": 0, "last_raw": 60}]}})
        snap = self.snapshot()
        report = audit.reconciliation(snap, snap, 60)["openvpn"]["accounting"]
        self.assertEqual(report["users_with_committed_reflection_receipt"], 0)
        self.assertEqual(report["awaiting_reflection_raw_bytes"], 140)
        watermark = json.dumps({"version": 2, "epoch": "e1", "totals": {json.dumps("openvpn") + ":" + json.dumps(tag): 80}})
        db = sqlite3.connect(self.args.sqlite)
        db.execute("INSERT INTO node_wireguard_usage_reflection VALUES(7,871234567,'ACK',?)", (watermark,))
        db.execute("INSERT INTO node_wireguard_usage_reflection VALUES(8,871234567,'ACK',?)", (watermark.replace("80", "140"),))
        db.commit()
        db.close()
        snap = self.snapshot()
        report = audit.reconciliation(snap, snap, 60)["openvpn"]["accounting"]
        self.assertEqual(report["users_with_committed_reflection_receipt"], 1)
        self.assertEqual(report["awaiting_reflection_raw_bytes"], 60)

    def test_runtime_missing_extra_and_traffic_zero_increment_are_flagged(self):
        before, after = self.snapshot(), self.snapshot()
        key = ("wireguard", 871234567, self.tags["wireguard"])
        extra = ("wireguard", 982345678, self.tags["wireguard"])
        for snap, total in ((before, 10), (after, 40)):
            live = snap["live"]["wireguard"]
            live.update(available=True, complete=True, loaded={extra}, active={key}, sessions=1,
                        series={"PRIVATE-label": (key, total)})
            snap["accounting"]["totals"][key] = 5
        after["live"]["wireguard"]["observed_monotonic"] += 60
        report = audit.reconciliation(before, after, 60)["wireguard"]
        self.assertEqual(report["runtime"]["missing_runtime_users"], 1)
        self.assertEqual(report["runtime"]["extra_runtime_users"], 1)
        self.assertEqual(report["accounting"]["native_counter_delta_bytes"], 30)
        self.assertEqual(report["accounting"]["traffic_without_logical_increment_candidates"], 1)

    def test_reconciliation_report_contains_no_identifiers_or_secrets(self):
        snap = self.snapshot()
        encoded = json.dumps(audit.reconciliation(snap, snap, 60))
        for secret in self.secrets + list(self.tags.values()) + ["871234567", "871234568", "OTHER-secret"]:
            self.assertNotIn(secret, encoded)

    def test_binary_metadata_never_executes_target(self):
        path = self.root / "antimage-node"
        path.write_bytes(b"fake binary")
        with mock.patch.object(audit.shutil, "which", return_value="go"), mock.patch.object(audit, "command", return_value=None) as run:
            audit.binary_info(str(path))
        run.assert_called_once_with(["go", "version", "-m", str(path)], timeout=5)

    def test_xray_stats_query_never_resets_counters_or_infers_online(self):
        inventory = audit.runtime_inventory(self.root)
        reply = subprocess.CompletedProcess([], 0, json.dumps({"stat": [{"name": "user>>>SECRET-uuid.hysteria>>>traffic>>>uplink", "value": 99}]}))
        with mock.patch.object(audit, "command", return_value=reply) as run:
            live = audit.live_xray(self.root, inventory, 10085)
        self.assertIn("-reset=false", run.call_args.args[0])
        self.assertIsNone(live["hysteria"]["loaded"])
        self.assertEqual(live["hysteria"]["active"], set())
        self.assertEqual(sum(v[1] for v in live["hysteria"]["series"].values()), 99)

    def test_wireguard_all_dump_columns_are_parsed_with_interface_prefix(self):
        self.write("wireguard/inbounds/runtime1/usage-helper.json", {"inbound_tag": self.tags["wireguard"],
            "interface_name": "wg-test", "peers": {"SECRET-public-key": 871234567}})
        row = "wg-test\tSECRET-public-key\tSECRET-psk\tPRIVATE-endpoint\t10.0.0.2/32\t0\t123\t456\t0\n"
        with mock.patch.object(audit.shutil, "which", return_value="wg"), mock.patch.object(audit, "command", return_value=subprocess.CompletedProcess([], 0, row)):
            live = audit.live_wireguard(self.root, audit.runtime_inventory(self.root), "wireguard")
        self.assertEqual(len(live["loaded"]), 1)
        self.assertEqual(sum(value for _, value in live["series"].values()), 579)

    def test_amneziawg_real_managed_helper_layout(self):
        self.write("amneziawg/runtime/runtime1/usage-helper.json", {"inbound_tag": self.tags["amneziawg"],
            "interface_name": "awg-test", "peers": {"SECRET-public-key": 871234567}})
        row = "awg-test\tSECRET-public-key\tSECRET-psk\tPRIVATE-endpoint\t10.0.0.2/32\t0\t123\t456\t0\n"
        with mock.patch.object(audit.shutil, "which", return_value="awg"), mock.patch.object(audit, "command", return_value=subprocess.CompletedProcess([], 0, row)):
            live = audit.live_wireguard(self.root, audit.runtime_inventory(self.root), "amneziawg")
        self.assertTrue(live["available"])
        self.assertEqual(len(live["loaded"]), 1)
        self.assertEqual(sum(value for _, value in live["series"].values()), 579)

    def test_unmapped_wireguard_dump_does_not_claim_zero_loaded_users(self):
        row = "wg-test\tSECRET-public-key\tSECRET-psk\tPRIVATE-endpoint\t10.0.0.2/32\t0\t123\t456\t0\n"
        with mock.patch.object(audit.shutil, "which", return_value="wg"), mock.patch.object(audit, "command", return_value=subprocess.CompletedProcess([], 0, row)):
            live = audit.live_wireguard(self.root, audit.runtime_inventory(self.root), "wireguard")
        self.assertFalse(live["available"])
        self.assertIsNone(live["loaded"])
        self.assertEqual(live["unmapped_accounts"], 1)

    def test_xray_uses_authoritative_runtime_port_unless_overridden(self):
        snap, _ = audit.read_json(self.root / "accounting/runtime-policy.json")
        config = json.loads(snap["config_json"])
        config["inbounds"].append({"tag": "API_INBOUND", "port": "12345", "protocol": "dokodemo-door"})
        snap["config_json"] = json.dumps(config)
        self.write("accounting/runtime-policy.json", snap)
        inventory = audit.runtime_inventory(self.root)
        self.assertEqual(inventory["xray_api_port"], 12345)
        with mock.patch.object(audit, "command", return_value=subprocess.CompletedProcess([], 0, '{"stat": []}')) as run:
            audit.live_xray(self.root, inventory, None)
            self.assertIn("-server=127.0.0.1:12345", run.call_args.args[0])
            audit.live_xray(self.root, inventory, 23456)
            self.assertIn("-server=127.0.0.1:23456", run.call_args.args[0])

    def test_ikev2_attributes_eap_identity_not_generic_client_id(self):
        raw = "list-sa event { conn { uniqueid=1 state=ESTABLISHED remote-id=client remote-eap-id=SECRET-username child-sas { child { bytes-in=100 bytes-out=200 } } } }"
        with mock.patch.object(audit, "command", return_value=subprocess.CompletedProcess([], 0, raw)):
            live = audit.live_ikev2(self.root, audit.runtime_inventory(self.root))
        self.assertEqual(live["sessions"], 1)
        self.assertEqual(len(live["active"]), 1)
        self.assertEqual(sum(value for _, value in live["series"].values()), 300)

    def test_read_limit_and_unknown_protocol_are_redacted(self):
        self.assertEqual(audit.protocol_name("SECRET-token"), "unknown")
        with mock.patch.object(audit, "MAX_FILE_BYTES", 2):
            obj, status = audit.read_json(self.root / "accounting/runtime-policy.json")
        self.assertIsNone(obj)
        self.assertEqual(status, "size_limit")

    def test_entire_report_is_redacted_and_cpu_is_node_process_only(self):
        before, after = self.snapshot(), self.snapshot()
        before.update(started_utc="before", finished_utc="before")
        after.update(started_utc="after", finished_utc="after")
        node = {"pid": 123, "exe": "antimage-node", "stat": {"ticks": 10, "start_ticks": 1},
                "rss_kib": 4096, "vsz_kib": 8192, "threads": 3}
        self.args.pid, self.args.sample_seconds, self.args.sample_interval = 0, 60, 2
        stream = io.StringIO()
        with contextlib.ExitStack() as stack:
            stack.enter_context(contextlib.redirect_stdout(stream))
            replacements = {"find_node": node, "process_info": node, "proc_stat": node["stat"],
                            "cpu_ticks": (1000, 500), "total_ram_bytes": 1 << 30,
                            "collect_system_cpu": {"logical_cpu_count": 2}, "process_limits": {},
                            "interval_configuration": {}, "binary_info": {}, "xray_version": None,
                            "disk_usage": {"rx_bytes": 100, "tx_bytes": 100}}
            for name, value in replacements.items():
                stack.enter_context(mock.patch.object(audit, name, return_value=value))
            stack.enter_context(mock.patch.object(audit, "audit_snapshot", side_effect=[before, after]))
            stack.enter_context(mock.patch.object(audit.os, "sysconf", return_value=100, create=True))
            stack.enter_context(mock.patch.object(audit.os, "readlink", return_value="same-netns"))
            stack.enter_context(mock.patch.object(audit.Path, "iterdir", return_value=iter(())))
            stack.enter_context(mock.patch.object(audit.time, "monotonic", side_effect=itertools.count()))
            stack.enter_context(mock.patch.object(audit.time, "sleep"))
            audit.sample(self.args)
        report = json.loads(stream.getvalue())
        self.assertGreaterEqual(report["observation_seconds"], 60)
        self.assertEqual(report["node_process"]["cpu_percent_one_core"], 0)
        self.assertEqual(set(report["protocol_reconciliation"]), set(audit.PROTOCOLS))
        for secret in self.secrets + list(self.tags.values()) + ["871234567", "871234568"]:
            self.assertNotIn(secret, stream.getvalue())


if __name__ == "__main__":
    unittest.main()
