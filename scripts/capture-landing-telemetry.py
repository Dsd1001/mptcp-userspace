#!/usr/bin/env python3
"""Record bounded per-Session/Carrier diagnostic snapshots without credentials.

Run on the Landing host alongside an independent TCP packet capture. The
published status file is read-only; no service settings or secrets are touched.
"""
import argparse
import datetime as dt
import json
import pathlib
import sys
import time

PATH_FIELDS = (
    "id", "connected", "role", "rtt_ms", "outstanding_bytes", "budget_bytes",
    "queue_bytes", "configured_rate_bps", "measured_delivery_bps", "goodput_bps",
    "sent", "received", "errors", "last_error",
)
RESOURCE_FIELDS = (
    "queue_admission_limit_bytes", "queue_admission_waiters", "ready_data_bytes",
    "data_pending_bytes", "session_lock_count", "session_lock_wait_ns",
    "session_lock_hold_ns", "dispatch_runs", "dispatch_frames", "dispatch_ns",
    "write_wait_reasons", "path_selection", "window_blocked_writers",
)
SESSION_FIELDS = (
    "sent", "received", "connections", "paths", "window_waits",
    "retransmits", "reorder_peak",
)


def sanitized_snapshot(status: dict) -> dict:
    sessions = []
    for s in status.get("tcp", []):
        resources = s.get("resources", {})
        sessions.append({
            "session_tag": s.get("lifecycle", {}).get("session_tag"),
            **{name: s.get(name) for name in SESSION_FIELDS},
            "resources": {name: resources.get(name) for name in RESOURCE_FIELDS},
            "carriers": [
                {name: p.get(name) for name in PATH_FIELDS}
                for p in s.get("path_stats", [])
            ],
        })
    return {
        "captured_at": dt.datetime.now(dt.timezone.utc).isoformat(),
        "status_updated": status.get("updated"),
        "version": status.get("version"),
        "sessions": sessions,
    }


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--status-file", default="/run/mptcp-userspace-landing/status.json")
    ap.add_argument("--output", type=pathlib.Path, required=True)
    ap.add_argument("--interval", type=float, default=2.0)
    ap.add_argument("--duration", type=float, default=360.0)
    args = ap.parse_args()
    if not 0.25 <= args.interval <= 30 or not 1 <= args.duration <= 86400:
        ap.error("--interval must be 0.25..30 s and --duration 1..86400 s")
    source = pathlib.Path(args.status_file)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    deadline = time.monotonic() + args.duration
    with args.output.open("w", encoding="utf-8") as f:
        while time.monotonic() < deadline:
            try:
                status = json.loads(source.read_text(encoding="utf-8"))
                row = sanitized_snapshot(status)
            except (OSError, ValueError, TypeError) as e:
                row = {"captured_at": dt.datetime.now(dt.timezone.utc).isoformat(),
                       "error": type(e).__name__}
            f.write(json.dumps(row, ensure_ascii=False, separators=(",", ":")) + "\n")
            f.flush()
            time.sleep(min(args.interval, max(0, deadline-time.monotonic())))
    return 0


if __name__ == "__main__":
    sys.exit(main())
