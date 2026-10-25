#!/usr/bin/env python3
"""Linux process capture for a REAL external mixed-protocol scenario.

No mock workload is supplied here. Missing scenario evidence is a failure, never
a CPU acceptance result. See docs/node-runtime-performance.md for the contract.
"""

import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import signal
import subprocess
import sys
import time


def require(condition, message):
    if not condition:
        raise ValueError(message)


def read_json(path):
    with open(path, encoding="utf-8") as stream:
        return json.load(stream)


def write_json(path, value):
    with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w", encoding="utf-8") as stream:
        json.dump(value, stream, indent=2, allow_nan=False)
        stream.write("\n")


def command(value):
    require(isinstance(value, list) and value and all(isinstance(x, str) for x in value),
            "commands must be nonempty argv arrays (no shell interpolation)")
    return value


def stop_group(process, grace=15):
    """Also signal descendants if their original parent has already exited."""
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        process.wait()
        return
    deadline = time.monotonic() + grace
    while time.monotonic() < deadline:
        process.poll()  # reap the group leader before probing the group
        try:
            os.killpg(process.pid, 0)
        except ProcessLookupError:
            process.wait()
            return
        time.sleep(0.1)
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    process.wait(timeout=5)


def process_sample(pid):
    # /proc/<pid>/stat fields 14/15 are SELF user/system ticks; child ticks
    # (Xray, daemons, traffic generators) must never enter NodeAgent CPU.
    fields = Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[1].split()
    status = {}
    for line in Path(f"/proc/{pid}/status").read_text().splitlines():
        if ":" in line:
            key, value = line.split(":", 1)
            status[key] = value.strip()
    return {
        "monotonic": time.monotonic(), "unix_seconds": time.time(), "pid": pid,
        "cpu_seconds": (int(fields[11]) + int(fields[12])) / os.sysconf("SC_CLK_TCK"),
        "start_ticks": int(fields[19]),
        "rss_bytes": int(status["VmRSS"].split()[0]) * 1024,
        "rss_peak_bytes": int(status["VmHWM"].split()[0]) * 1024,
        "threads": int(status["Threads"]),
    }


def nonnegative_int(value):
    return type(value) is int and value >= 0


def owner_snapshot(snapshot, expected):
    owners = {}
    for owner in snapshot["owners"]:
        key = (owner["protocol"], owner["user_id"], owner["inbound_tag"])
        require(key not in owners, f"duplicate accounting owner: {key}")
        require(type(key[1]) is int and key[1] > 0 and key[0] and key[2], "invalid owner identity")
        for field in ("generated_payload_bytes", "source_raw_bytes", "node_raw_bytes",
                      "controller_raw_bytes", "node_pending_raw_bytes"):
            require(nonnegative_int(owner[field]), f"invalid {field}: {key}")
        require(owner["generated_payload_bytes"] <= owner["source_raw_bytes"], f"traffic missing: {key}")
        require(owner["source_raw_bytes"] == owner["node_raw_bytes"], f"source/node bytes differ: {key}")
        require(owner["node_raw_bytes"] == owner["controller_raw_bytes"] + owner["node_pending_raw_bytes"],
                f"node/panel conservation failed: {key}")
        require(type(owner["active"]) is bool and type(owner["runtime_present"]) is bool,
                "runtime membership must be independently enumerated")
        require(owner["runtime_present"], f"configured user missing from runtime: {key}")
        owners[key] = owner
    require(set(owners) == expected, "missing/extra runtime or accounting owners")
    return owners


def validate_workload(evidence, config, actual_restarts):
    require(evidence["schema_version"] == 1, "unsupported workload evidence")
    expected = {(o["protocol"], o["user_id"], o["inbound_tag"]) for o in evidence["inventory"]}
    require(len(expected) == len(evidence["inventory"]), "duplicate inventory owners")
    require(len({k[1] for k in expected}) >= config["users"], "workload user count below contract")
    require(len({k[2] for k in expected}) >= config["inbounds"], "insufficient inbound mix")
    protocols = {k[0] for k in expected}
    require("xray" in protocols and len(protocols) >= 2, "real Xray and native protocols required")
    require(set(config["protocols"]) <= protocols, "required protocol absent")
    require(evidence["node_executable_sha256"] == config["node_executable_sha256"], "wrong node executable")
    require(evidence["source_revision"] == config["source_revision"], "wrong source revision")
    require(evidence["quota_interval_seconds"] == config["quota_interval_seconds"], "quota cadence changed")
    # The adapter must count completed calls/queries, not configured intentions.
    for name in ("accounting_ticks", "quota_evaluations", "controller_syncs", "db_queries",
                 "reflection_acks", "reconnects", "controller_restarts", "quota_denials"):
        require(nonnegative_int(evidence["counts"][name]) and evidence["counts"][name] > 0,
                f"missing executed workload activity: {name}")
    require(evidence["counts"]["node_restarts"] == actual_restarts and actual_restarts > 0,
            "node restart evidence differs from captured processes")
    snapshots = evidence["snapshots"]
    require(snapshots[0]["phase"] == "baseline" and snapshots[-1]["phase"] == "final", "incomplete run")
    previous = None
    replay_pairs = restart_pairs = 0
    for index, snapshot in enumerate(snapshots):
        owners = owner_snapshot(snapshot, expected)
        require(any(o["active"] for o in owners.values()) and any(not o["active"] for o in owners.values()),
                "active and inactive users required")
        if previous is not None:
            for key, owner in owners.items():
                for field in ("generated_payload_bytes", "source_raw_bytes", "node_raw_bytes", "controller_raw_bytes"):
                    require(owner[field] >= previous[key][field], f"counter regressed across restart/replay: {key}")
        if snapshot["phase"] in ("post_lost_ack_replay", "post_restart"):
            pre = "pre_lost_ack_replay" if snapshot["phase"] == "post_lost_ack_replay" else "pre_restart"
            require(index > 0 and snapshots[index - 1]["phase"] == pre, "missing quiescent pre-checkpoint")
            # No traffic during this pair. Panel has already committed before
            # the ACK is dropped; replay must not change ANY cumulative bytes.
            for key, owner in owners.items():
                for field in ("generated_payload_bytes", "source_raw_bytes", "node_raw_bytes", "controller_raw_bytes"):
                    require(owner[field] == previous[key][field], f"replay/restart changed accounting: {key}")
            replay_pairs += snapshot["phase"] == "post_lost_ack_replay"
            restart_pairs += snapshot["phase"] == "post_restart"
        previous = owners
    require(replay_pairs > 0 and restart_pairs >= actual_restarts, "missing lost-ACK/restart assertions")
    baseline = owner_snapshot(snapshots[0], expected)
    for key, owner in previous.items():
        require(owner["node_pending_raw_bytes"] == 0, f"final usage not settled: {key}")
    for protocol in protocols:
        require(sum(o["generated_payload_bytes"] - baseline[k]["generated_payload_bytes"]
                    for k, o in previous.items() if k[0] == protocol) > 0,
                f"no real generated traffic for {protocol}")
    return {"owners": len(expected), "users": len({k[1] for k in expected}),
            "total_raw_bytes": sum(o["source_raw_bytes"] - baseline[k]["source_raw_bytes"]
                                   for k, o in previous.items()),
            "lost_ack_pairs": replay_pairs, "restart_pairs": restart_pairs,
            "counts": evidence["counts"]}


def summarize_process(samples, warmup):
    cpu = wall = 0.0
    steady = []
    for before, after in zip(samples, samples[1:]):
        if (after["segment"] != before["segment"] or before["age_seconds"] < warmup):
            continue
        delta = after["monotonic"] - before["monotonic"]
        require(delta > 0 and delta <= 5, "sampling gap in measured steady state")
        require(before["start_ticks"] == after["start_ticks"], "PID identity changed")
        used = after["cpu_seconds"] - before["cpu_seconds"]
        require(used >= 0, "CPU counter reset within segment")
        cpu += used
        wall += delta
        steady.append({"cpu_percent": 100 * used / delta, "rss_bytes": after["rss_bytes"]})
    require(wall > 0 and steady, "no steady-state samples")
    return {"steady_seconds": wall, "node_cpu_seconds": cpu,
            "node_cpu_percent_one_core": 100 * cpu / wall,
            "max_rss_bytes": max(x["rss_bytes"] for x in steady),
            "max_interval_cpu_percent": max(x["cpu_percent"] for x in steady)}


def summarize_runtime(segment_dirs, process_samples, warmup, revision):
    segments = []
    for segment, directory in enumerate(segment_dirs):
        captured = [s for s in process_samples if s["segment"] == segment and s["age_seconds"] >= warmup]
        require(len(captured) >= 2, "missing process interval for runtime capture")
        build = read_json(directory / "build.json")
        require(build["pid"] == captured[0]["pid"], "runtime/process capture identity differs")
        settings = {s["Key"]: s["Value"] for s in build["build"]["Settings"]}
        require(settings.get("vcs.revision") == revision and settings.get("vcs.modified") == "false",
                "capture binary must be built from the exact clean source revision")
        with open(directory / "runtime.jsonl", encoding="utf-8") as stream:
            all_rows = [json.loads(line) for line in stream]
        rows = [r for r in all_rows if captured[0]["unix_seconds"] <= r["unix_seconds"] <= captured[-1]["unix_seconds"]]
        require(len(rows) >= 2, f"missing runtime capture: {directory}")
        for name in ("cpu", "heap", "allocs", "goroutine"):
            require((directory / (name + ".pprof")).stat().st_size > 0, f"missing {name} profile")
        start, end = rows[0], rows[-1]
        seconds = end["elapsed_seconds"] - start["elapsed_seconds"]
        require(seconds > 0, "empty runtime interval")
        delta = lambda name: end["metrics"][name] - start["metrics"][name]
        allocated = delta("/gc/heap/allocs:bytes")
        gc = delta("/gc/cycles/total:gc-cycles")
        gc_cpu = delta("/cpu/classes/gc/total:cpu-seconds")
        require(min(allocated, gc, gc_cpu) >= 0, "runtime counter reset")
        # Trend is evidence, not an automatic claim that every heap increase is
        # a leak. Compare equivalent segments and inspect retained-heap profiles.
        def slope(metric):
            xs = [r["elapsed_seconds"] for r in rows]
            ys = [r["metrics"][metric] for r in rows]
            mean_x, mean_y = sum(xs) / len(xs), sum(ys) / len(ys)
            variance = sum((x - mean_x) ** 2 for x in xs)
            return sum((x - mean_x) * (y - mean_y) for x, y in zip(xs, ys)) / variance

        segments.append({"seconds": seconds, "allocated_bytes": allocated,
                         "allocation_bytes_per_second": allocated / seconds,
                         "gc_cycles": gc, "gc_cpu_seconds": gc_cpu,
                         "max_heap_objects_bytes": max(r["metrics"]["/memory/classes/heap/objects:bytes"] for r in rows),
                         "max_goroutines": max(r["metrics"]["/sched/goroutines:goroutines"] for r in rows),
                         "live_heap_bytes_per_second_trend": slope("/gc/heap/live:bytes"),
                         "goroutines_per_second_trend": slope("/sched/goroutines:goroutines"),
                         "initial_live_heap_bytes": start["metrics"]["/gc/heap/live:bytes"],
                         "final_live_heap_bytes": end["metrics"]["/gc/heap/live:bytes"]})
    return segments


def validate_config(config):
    require(config["users"] >= 1000, "minimum 1000 users; use the actual higher production scale")
    require(config["inbounds"] >= 4, "minimum four inbounds")
    require("xray" in config["protocols"] and len(set(config["protocols"])) >= 2, "mixed real protocols required")
    require(config["warmup_seconds"] >= 60 and config["duration_seconds"] >= 900, "insufficient run duration")
    require(config["minimum_steady_seconds"] >= 600, "insufficient measured steady state")
    require(config["quota_interval_seconds"] > 0, "record the unmodified production quota interval")
    require(config["restart_seconds"] and sorted(set(config["restart_seconds"])) == config["restart_seconds"],
            "provide ordered unique restart times")
    boundaries = [0, *config["restart_seconds"], config["duration_seconds"]]
    require(all(b - a >= config["warmup_seconds"] + 60 for a, b in zip(boundaries, boundaries[1:])),
            "each process segment needs warmup plus steady measurements")
    for key in ("prepare", "ready", "workload", "cleanup", "verify_cleanup", "node"):
        command(config[key])


def run(config, output):
    require(sys.platform == "linux", "a suitable Linux runtime is mandatory")
    validate_config(config)
    executable = Path(config["node"][0]).resolve(strict=True)
    config["node_executable_sha256"] = hashlib.sha256(executable.read_bytes()).hexdigest()
    output.mkdir(mode=0o700)  # new evidence only; never overwrite a previous run
    env = dict(os.environ, **config.get("env", {}), ANTIMAGE_PERF_RUN_DIR=str(output))
    processes, logs, samples, directories = [], [], [], []
    node = workload = None

    def launch(argv, label, extra=None):
        log = os.fdopen(os.open(output / (label + ".log"), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "wb")
        logs.append(log)
        process = subprocess.Popen(command(argv), env=dict(env, **(extra or {})),
                                   stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
        processes.append(process)
        return process

    def stage(name, timeout=300):
        process = launch(config[name], name)
        try:
            require(process.wait(timeout=timeout) == 0, f"{name} failed; inspect {name}.log")
        finally:
            stop_group(process)
            processes.remove(process)

    def start_node(segment):
        directory = output / f"node-{segment}"
        directories.append(directory)
        return launch(config["node"], f"node-{segment}", {
            "ANTIMAGE_PERF_OUTPUT": str(directory),
            "ANTIMAGE_PERF_WARMUP_SECONDS": str(config["warmup_seconds"]),
            "ANTIMAGE_PERF_START_FILE": str(directory / "measurement-start"),
        })

    def interrupted(_signum, _frame):
        raise InterruptedError("performance run cancelled")

    old_handlers = {sig: signal.signal(sig, interrupted) for sig in (signal.SIGTERM, signal.SIGINT)}
    failure = None
    restarts = segment = 0
    try:
        # Persist reproducibility metadata without TLS keys/password env values.
        write_json(output / "contract.json", {k: v for k, v in config.items() if k != "env"})
        stage("prepare")
        node = start_node(segment)
        stage("ready")
        workload = launch(config["workload"], "workload")
        start = time.monotonic()
        segment_start = start
        (directories[-1] / "measurement-start").touch(exist_ok=False)
        restart_at = iter(config["restart_seconds"])
        next_restart = next(restart_at, math.inf)
        with os.fdopen(os.open(output / "process.jsonl", os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w", encoding="utf-8") as stream:
            while time.monotonic() - start < config["duration_seconds"]:
                require(node.poll() is None, "NodeAgent exited unexpectedly")
                require(workload.poll() is None, "workload exited before the measured interval finished")
                row = process_sample(node.pid)
                row.update(segment=segment, age_seconds=time.monotonic() - segment_start)
                samples.append(row)
                stream.write(json.dumps(row) + "\n")
                stream.flush()
                if time.monotonic() - start >= next_restart:
                    # Adapter quiesces traffic and commits a pre_restart checkpoint
                    # before publishing restart-ready-N, then reconnects afterwards.
                    marker = output / f"restart-ready-{restarts + 1}"
                    deadline = time.monotonic() + 60
                    while not marker.is_file():
                        require(time.monotonic() < deadline, "workload did not quiesce for restart")
                        require(workload.poll() is None and node.poll() is None, "process exited during restart handshake")
                        time.sleep(0.1)
                    stop_group(node)
                    require(node.returncode == 0, "NodeAgent did not shut down cleanly")
                    processes.remove(node)
                    restarts += 1
                    segment += 1
                    node = start_node(segment)
                    # Avoid duplicate ready.log names after restart.
                    ready = launch(config["ready"], f"ready-{segment}")
                    try:
                        require(ready.wait(timeout=300) == 0, "NodeAgent did not become ready after restart")
                    finally:
                        stop_group(ready)
                        processes.remove(ready)
                    segment_start = time.monotonic()
                    (directories[-1] / "measurement-start").touch(exist_ok=False)
                    (output / f"restart-complete-{restarts}").touch(exist_ok=False)
                    next_restart = next(restart_at, math.inf)
                time.sleep(1)
        (output / "finish-requested").touch(exist_ok=False)
        require(workload.wait(timeout=120) == 0, "workload failed final reconciliation")
        stop_group(node)
        require(node.returncode == 0, "NodeAgent shutdown failed")
        processes.remove(node)
        process_result = summarize_process(samples, config["warmup_seconds"])
        require(process_result["steady_seconds"] >= config["minimum_steady_seconds"], "too little measured steady state")
        accounting = validate_workload(read_json(output / "workload.json"), config, restarts)
        runtime = summarize_runtime(directories, samples, config["warmup_seconds"], config["source_revision"])
        write_json(output / "measurement.json", {"process": process_result, "runtime_segments": runtime,
                                               "accounting": accounting,
                                               "cpu_under_20_percent": process_result["node_cpu_percent_one_core"] < 20,
                                               "acceptance": "REQUIRES_MEMORY_TREND_AND_WORKLOAD_EVIDENCE_REVIEW"})
    except BaseException as exc:
        failure = exc
    finally:
        for sig in old_handlers:
            signal.signal(sig, signal.SIG_IGN)
        for process in reversed(processes):
            try:
                stop_group(process)
            except BaseException as exc:
                failure = failure or exc
        try:
            stage("cleanup", 120)
            stage("verify_cleanup", 60)
        except BaseException as exc:
            failure = failure or exc
        for log in logs:
            log.close()
        for sig, handler in old_handlers.items():
            signal.signal(sig, handler)
    if failure:
        write_json(output / "failure.json", {"error": str(failure), "type": type(failure).__name__})
        raise failure


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--scenario", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    run(read_json(args.scenario), args.output.resolve())


if __name__ == "__main__":
    main()
