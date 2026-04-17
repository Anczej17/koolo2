"""dump_phase.py — snapshot ONE timepoint into an existing uber_dumps session.

Used for user-driven step-by-step capture: I run this between user actions.

Usage:
    python tools/dump_phase.py --port N --char Blizzard --session 20260415_HHMMSS --label t0
"""
from __future__ import annotations

import argparse
import json
import sys
import time
from pathlib import Path
from datetime import datetime

# Import shared helpers from uber_dump
sys.path.insert(0, str(Path(__file__).resolve().parent))
from uber_dump import (
    REGIONS, get_json, get_text, dump_all_regions, snapshot_state,
    get_active_d2r_pid, bot_log_path,
)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, required=True)
    ap.add_argument("--char", default="Blizzard")
    ap.add_argument("--session", required=True, help="session id (timestamp) under build/uber_dumps/uber_<id>")
    ap.add_argument("--label", required=True, help="snapshot label, e.g. t0_baseline / t1_stash_open / t2_post_hid / t3_post_send")
    ap.add_argument("--note", default="", help="optional note saved alongside snapshot")
    args = ap.parse_args()

    out_dir = Path("build/uber_dumps") / f"uber_{args.session}"
    out_dir.mkdir(parents=True, exist_ok=True)
    label = args.label
    label_dir = out_dir / label
    label_dir.mkdir(parents=True, exist_ok=True)

    pid = get_active_d2r_pid()
    print(f"[dump-phase] session={args.session} label={label} d2r_pid={pid}")

    # snapshot bot-side state first (json), then memory regions
    snapshot_state(args.port, args.char, out_dir, label)
    sizes = dump_all_regions(args.port, args.char, out_dir, label)

    meta = {
        "label": label,
        "ts": datetime.now().isoformat(),
        "d2r_pid": pid,
        "note": args.note,
        "region_sizes": sizes,
    }
    (label_dir / "_meta.json").write_text(json.dumps(meta, indent=2))
    print(f"[dump-phase] OK -> {label_dir}")


if __name__ == "__main__":
    main()
