#!/usr/bin/env bash
# Per-section D2R dump + analysis runner.
#
# Usage:
#   ./tools/section_dump.sh baseline 4172
#   ./tools/section_dump.sh sec01_walk 4172
#   ./tools/section_dump.sh sec02_run 4172
#
# Each invocation:
#   1. Dumps current D2R executable memory to logs/section_<name>.{bin,json}
#   2. Runs analyzer, prints all callers
#   3. Compares to all-known-so-far call site list, prints only NEW ones
#   4. Updates the cumulative known list
set -e
NAME=$1
PID=$2
if [ -z "$NAME" ] || [ -z "$PID" ]; then
    echo "usage: $0 <section_name> <pid>"
    exit 1
fi

OUT="logs/section_${NAME}"
echo "# === section: $NAME (D2R pid=$PID) ==="
python tools/d2r_dump_exec.py --pid "$PID" --out "$OUT" 2>&1 | tail -3
echo ""
python tools/d2r_section_analyze.py --prefix "$OUT" --section "$NAME"
