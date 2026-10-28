import copy
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import node_runtime_load as load


def fixture():
    inventory = [{"protocol": "xray" if i % 2 else "wg", "user_id": i,
                  "inbound_tag": f"in-{i % 4}"} for i in range(1, 1001)]
    owners = [dict(o, active=bool(o["user_id"] % 3), runtime_present=True,
                   generated_payload_bytes=0, source_raw_bytes=0, node_raw_bytes=0,
                   controller_raw_bytes=0, node_pending_raw_bytes=0) for o in inventory]
    settled = copy.deepcopy(owners)
    for o in settled:
        if o["active"]:
            o.update(generated_payload_bytes=80, source_raw_bytes=100,
                     node_raw_bytes=100, controller_raw_bytes=100)
    evidence = {
        "schema_version": 1, "inventory": inventory, "node_executable_sha256": "test-hash",
        "source_revision": "test-revision", "quota_interval_seconds": 1,
        "counts": dict.fromkeys(("accounting_ticks", "quota_evaluations", "controller_syncs", "db_queries",
                                 "reflection_acks", "reconnects", "node_restarts", "controller_restarts", "quota_denials"), 1),
        "snapshots": [{"phase": "baseline", "owners": owners}] +
        [{"phase": phase, "owners": copy.deepcopy(settled)} for phase in
         ("pre_lost_ack_replay", "post_lost_ack_replay", "pre_restart", "post_restart", "final")],
    }
    config = {"users": 1000, "inbounds": 4, "protocols": ["xray", "wg"],
              "node_executable_sha256": "test-hash", "source_revision": "test-revision", "quota_interval_seconds": 1}
    return evidence, config


class ConservationTests(unittest.TestCase):
    def test_valid_multilayer_reconciliation(self):
        evidence, config = fixture()
        result = load.validate_workload(evidence, config, 1)
        self.assertEqual(result["users"], 1000)
        self.assertEqual(result["total_raw_bytes"], 66700)
        self.assertEqual(result["lost_ack_pairs"], 1)
        self.assertEqual(result["restart_pairs"], 1)

    def test_missing_extra_duplicate_and_wrong_attribution_fail(self):
        for kind in ("missing", "extra", "duplicate", "wrong attribution"):
            with self.subTest(kind=kind):
                evidence, config = fixture()
                owners = evidence["snapshots"][-1]["owners"]
                if kind == "missing":
                    owners.pop()
                elif kind == "extra":
                    owners.append(dict(owners[0], user_id=1001))
                elif kind == "duplicate":
                    owners.append(dict(owners[0]))
                else:
                    # Global sum is conserved, but per-user attribution is wrong.
                    owners[0]["controller_raw_bytes"] -= 10
                    owners[1]["controller_raw_bytes"] += 10
                with self.assertRaises(ValueError):
                    load.validate_workload(evidence, config, 1)

    def test_missing_activity_is_not_acceptance(self):
        for name in fixture()[0]["counts"]:
            with self.subTest(name=name):
                evidence, config = fixture()
                evidence["counts"][name] = 0
                with self.assertRaises(ValueError):
                    load.validate_workload(evidence, config, 1)

    def test_lost_ack_double_billing_and_restart_loss_fail(self):
        for phase in ("post_lost_ack_replay", "post_restart"):
            with self.subTest(phase=phase):
                evidence, config = fixture()
                snapshot = next(s for s in evidence["snapshots"] if s["phase"] == phase)
                # Even changing all layers together cannot hide duplication/loss.
                for name in ("generated_payload_bytes", "source_raw_bytes", "node_raw_bytes", "controller_raw_bytes"):
                    snapshot["owners"][0][name] += 1
                with self.assertRaises(ValueError):
                    load.validate_workload(evidence, config, 1)

    def test_no_mock_scale_reduction_or_wrong_revision(self):
        for field, value in (("users", 1001), ("inbounds", 5), ("protocols", ["xray", "awg"]),
                             ("source_revision", "wrong"), ("node_executable_sha256", "wrong"),
                             ("quota_interval_seconds", 2)):
            with self.subTest(field=field):
                evidence, config = fixture()
                config[field] = value
                with self.assertRaises(ValueError):
                    load.validate_workload(evidence, config, 1)

    def test_acknowledged_but_unreflected_pending_bytes_are_conserved(self):
        evidence, config = fixture()
        owner = evidence["snapshots"][1]["owners"][0]
        owner["controller_raw_bytes"] = 90
        owner["node_pending_raw_bytes"] = 10
        self.assertEqual(len(load.owner_snapshot(evidence["snapshots"][1],
                                               {(o["protocol"], o["user_id"], o["inbound_tag"]) for o in evidence["inventory"]})), 1000)
        # Final must be drained; raw ACK alone is insufficient for this invariant.
        evidence["snapshots"][-1]["owners"][0].update(controller_raw_bytes=90, node_pending_raw_bytes=10)
        with self.assertRaises(ValueError):
            load.validate_workload(evidence, config, 1)

    def test_integer_bytes_not_float_are_required(self):
        evidence, config = fixture()
        evidence["snapshots"][-1]["owners"][0]["node_raw_bytes"] = 100.0
        with self.assertRaises(ValueError):
            load.validate_workload(evidence, config, 1)


class ProcessTests(unittest.TestCase):
    def test_runtime_evidence_is_trimmed_and_bound_to_clean_revision_and_pid(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            build = {"pid": 42, "build": {"Settings": [
                {"Key": "vcs.revision", "Value": "revision"},
                {"Key": "vcs.modified", "Value": "false"}]}}
            (directory / "build.json").write_text(json.dumps(build))
            metrics = {"/gc/heap/allocs:bytes": 1000, "/gc/cycles/total:gc-cycles": 2,
                       "/cpu/classes/gc/total:cpu-seconds": .1,
                       "/memory/classes/heap/objects:bytes": 800,
                       "/sched/goroutines:goroutines": 10, "/gc/heap/live:bytes": 600}
            with open(directory / "runtime.jsonl", "w") as stream:
                for i in range(10):
                    stream.write(json.dumps({"unix_seconds": 100 + i, "elapsed_seconds": i,
                                             "metrics": {k: v * (i + 1) for k, v in metrics.items()}}) + "\n")
            for name in ("cpu", "heap", "allocs", "goroutine"):
                (directory / (name + ".pprof")).write_bytes(b"profile")
            samples = [{"segment": 0, "age_seconds": 60 + i, "unix_seconds": 102 + i, "pid": 42}
                       for i in range(3)]
            result = load.summarize_runtime([directory], samples, 60, "revision")[0]
            self.assertEqual(result["seconds"], 2)
            self.assertEqual(result["allocated_bytes"], 2000)
            self.assertEqual(result["gc_cycles"], 4)
            self.assertEqual(result["live_heap_bytes_per_second_trend"], 600)
            with self.assertRaises(ValueError):
                load.summarize_runtime([directory], samples, 60, "wrong")
            samples[0]["pid"] = 43
            with self.assertRaises(ValueError):
                load.summarize_runtime([directory], samples, 60, "revision")

    def test_cpu_is_self_only_and_never_averaged_over_restarts(self):
        samples = []
        for segment in range(2):
            samples += [{"segment": segment, "monotonic": segment * 10 + i,
                         "age_seconds": i + 60, "cpu_seconds": i * .1,
                         "start_ticks": segment + 1, "rss_bytes": 1000}
                        for i in range(4)]
        summary = load.summarize_process(samples, 60)
        self.assertAlmostEqual(summary["steady_seconds"], 6)
        self.assertAlmostEqual(summary["node_cpu_percent_one_core"], 10)

    def test_sampling_gap_or_pid_reuse_fails(self):
        before = {"segment": 0, "monotonic": 0, "age_seconds": 60, "cpu_seconds": 0,
                  "start_ticks": 1, "rss_bytes": 1000}
        for change in ({"monotonic": 9}, {"start_ticks": 2}, {"cpu_seconds": -1}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                load.summarize_process([before, dict(before, monotonic=1, **change)] if "monotonic" not in change
                                       else [before, dict(before, **change)], 60)

    @unittest.skipUnless(sys.platform == "linux", "requires real /proc and process groups")
    def test_read_real_self_process(self):
        sample = load.process_sample(os.getpid())
        self.assertGreater(sample["rss_bytes"], 0)
        self.assertGreaterEqual(sample["cpu_seconds"], 0)
        self.assertGreater(sample["start_ticks"], 0)

    @unittest.skipUnless(sys.platform == "linux", "requires real Linux process groups")
    def test_cancellation_reaps_driver_and_terminates_pipe_holding_child(self):
        with tempfile.TemporaryDirectory() as directory:
            marker = Path(directory) / "child.pid"
            # Parent reaps its child on SIGTERM. Both share the group owned by
            # the harness; the child holds an inherited stdout pipe open.
            code = ("import signal,subprocess,time,pathlib,sys; "
                    "p=subprocess.Popen([sys.executable,'-c','import time; time.sleep(300)']); "
                    "pathlib.Path(sys.argv[1]).write_text(str(p.pid)); "
                    "signal.signal(signal.SIGTERM,lambda *_:(p.wait(),sys.exit(0))); time.sleep(300)")
            process = subprocess.Popen([sys.executable, "-c", code, str(marker)], start_new_session=True,
                                       stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            try:
                deadline = time.monotonic() + 5
                while not marker.exists():
                    self.assertLess(time.monotonic(), deadline)
                    time.sleep(.01)
                child = int(marker.read_text())
                started = time.monotonic()
                load.stop_group(process, grace=2)
                self.assertLess(time.monotonic() - started, 4)
                self.assertEqual(process.returncode, 0)
                self.assertEqual(process.stdout.read(), b"")
                with self.assertRaises(ProcessLookupError):
                    os.kill(child, 0)
            finally:
                load.stop_group(process, grace=1)
                process.stdout.close()


if __name__ == "__main__":
    unittest.main()
