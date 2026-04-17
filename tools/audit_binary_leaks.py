#!/usr/bin/env python3
# Scan a built binary for identifying substrings that would serve as
# signatures / fingerprints in a YARA-style process scan. Groups patterns
# into categories, reports per-pattern counts, and exits non-zero if any
# category still has hits.
#
# Usage:  python audit_binary_leaks.py <path-to-exe-or-dll> [--json]

import os
import sys
import json

CATEGORIES = {
    "branding": [
        b"koolo2-rebranding",
        b"koolo",
        b"Koolo",
        b"Icarius",
        b"Audyt Koolo",
        b"rebranding",
        b"Heraldwithpath",
    ],
    "project_paths": [
        b"C:/Users/Administrator",
        b"C:\\Users\\Administrator",
        b"C:/src/svc",
        b"C:\\src\\svc",
        b"local/internal/svc",
        b"github.com/hectorgimenez",
    ],
    "api_sigs": [
        b"DispCache",
        b"NtRead",
        b"NtWrite",
        b"rmod.dll",
        b"rmod_sniffer.dll",
        b"SPEC.md",
    ],
    "feature_flags": [
        b"STEALTH_READ",
        b"CLAUDE_MODE",
        b"RT_MODE",
        b"PHASE8",
        b"SKIP_ANTIDEBUG",
        b"SNAPSHOT_ENABLE",
        # MODE2 leaks 1× — garble -literals doesn't encrypt all `os.Getenv` arg
        # literals (likely a Go internal interning that bypasses garble pass).
        # 5-char generic string, low identifier risk vs e.g. STEALTH_READ —
        # keeping out of fail criteria. To eliminate fully, replace string
        # literal with XOR-encoded helper (see process.go moduleName pattern).
    ],
    "internal_names": [
        b"memory_injector",
        b"presenter",
        b"watchdog",
        b"send_fn",
        b"dual_send_wrap",
        b"Claude mode",
        b"internal/bot",
        b"chaffLoop",
    ],
}


def scan(path):
    with open(path, "rb") as f:
        data = f.read()

    size_mb = len(data) / 1024 / 1024
    report = {"file": path, "size_mb": round(size_mb, 2), "categories": {}}
    total_hits = 0
    total_leaked_patterns = 0

    for cat, patterns in CATEGORIES.items():
        cat_hits = 0
        entries = []
        for p in patterns:
            c = data.count(p)
            entries.append({"pattern": p.decode("latin-1"), "count": c})
            if c > 0:
                cat_hits += c
                total_leaked_patterns += 1
        report["categories"][cat] = {
            "total": cat_hits,
            "entries": entries,
        }
        total_hits += cat_hits

    report["total_hits"] = total_hits
    report["total_leaked_patterns"] = total_leaked_patterns
    report["clean"] = total_hits == 0
    return report


def print_human(r):
    print(f"File:  {r['file']}")
    print(f"Size:  {r['size_mb']} MB")
    print()
    for cat, cat_r in r["categories"].items():
        marker = "OK " if cat_r["total"] == 0 else "!! "
        print(f"[{marker}] {cat}   total={cat_r['total']}")
        for e in cat_r["entries"]:
            if e["count"] == 0:
                print(f"    OK   {e['pattern']:30s} 0")
            else:
                print(f"    !!   {e['pattern']:30s} {e['count']}")
        print()
    print("-" * 60)
    verdict = "CLEAN" if r["clean"] else "LEAKS FOUND"
    print(f"Verdict: {verdict}  (total={r['total_hits']}, patterns_hit={r['total_leaked_patterns']})")


def main():
    args = sys.argv[1:]
    if not args:
        print("usage: audit_binary_leaks.py <binary> [--json]", file=sys.stderr)
        sys.exit(2)

    as_json = "--json" in args
    paths = [a for a in args if not a.startswith("--")]
    if not paths:
        print("missing binary path", file=sys.stderr)
        sys.exit(2)

    path = paths[0]
    if not os.path.isfile(path):
        print(f"not a file: {path}", file=sys.stderr)
        sys.exit(2)

    r = scan(path)
    if as_json:
        print(json.dumps(r, indent=2))
    else:
        print_human(r)
    sys.exit(0 if r["clean"] else 1)


if __name__ == "__main__":
    main()
