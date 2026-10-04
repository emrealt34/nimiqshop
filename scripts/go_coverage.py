#!/usr/bin/env python3
"""Audit complete Go profiles without exclusions or rounded threshold checks."""

import argparse
from collections import defaultdict
from decimal import Decimal
import json
from pathlib import Path
import re

BLOCK = re.compile(r"^(.+):(\d+\.\d+,\d+\.\d+) (\d+) (\d+)$")


def summarize(text):
    lines = text.splitlines()
    if not lines or lines[0] not in ("mode: atomic", "mode: count", "mode: set"):
        raise ValueError("Missing or invalid Go coverage mode")
    blocks = {}
    for line in lines[1:]:
        match = BLOCK.fullmatch(line)
        if not match:
            raise ValueError(f"Invalid coverage record: {line!r}")
        filename, location, statements, hits = match.groups()
        statements, hits = int(statements), int(hits)
        key = (filename, location)
        if key in blocks:
            old_statements, old_hits = blocks[key]
            if old_statements != statements:
                raise ValueError(f"Conflicting statement counts for {key}")
            hits += old_hits
        blocks[key] = (statements, hits)
    total = sum(statements for statements, _ in blocks.values())
    if not total:
        raise ValueError("Empty statement inventory is not 100% coverage")
    covered = sum(statements for statements, hits in blocks.values() if hits)
    files = defaultdict(lambda: {"total": 0, "covered": 0, "uncovered_blocks": []})
    for (filename, location), (statements, hits) in sorted(blocks.items()):
        entry = files[filename]
        entry["total"] += statements
        if hits:
            entry["covered"] += statements
        elif statements:
            entry["uncovered_blocks"].append(location)
    return {"covered": covered, "total": total, "uncovered": total - covered,
            "target_percent": 100, "target_met": covered == total,
            "percent": str(Decimal(covered) * 100 / Decimal(total)),
            "files": dict(files)}


def meets_minimum(report, minimum):
    minimum = Decimal(minimum)
    if not minimum.is_finite() or not 0 <= minimum <= 100:
        raise ValueError("Minimum must be a finite percentage between 0 and 100")
    return Decimal(report["covered"]) * 100 >= minimum * report["total"]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("profile", type=Path)
    parser.add_argument("--minimum", default="100")
    parser.add_argument("--json", type=Path)
    parser.add_argument("--summary", type=Path)
    args = parser.parse_args()
    report = summarize(args.profile.read_text(encoding="utf-8"))
    passed = meets_minimum(report, args.minimum)
    status = "ACHIEVED" if report["target_met"] else "NOT ACHIEVED"
    summary = [f"### Whole-backend coverage: {Decimal(report['percent']):.4f}%",
               f"- Covered: {report['covered']} / {report['total']} statements",
               f"- Uncovered: {report['uncovered']}",
               f"- Full-backend 100% target: **{status}**",
               f"- This run's minimum: {args.minimum}%",
               "- No source files or packages are excluded by this report.", "",
               "| File | Uncovered statements |", "| --- | ---: |"]
    ranked = sorted(report["files"].items(),
                    key=lambda item: item[1]["total"] - item[1]["covered"], reverse=True)
    for filename, data in ranked[:25]:
        missing = data["total"] - data["covered"]
        if missing:
            summary.append(f"| `{filename}` | {missing} |")
    text = "\n".join(summary) + "\n"
    print(text)
    if args.json:
        args.json.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    if args.summary:
        with args.summary.open("a", encoding="utf-8") as handle:
            handle.write(text)
    if not passed:
        raise SystemExit(f"Coverage minimum {args.minimum}% not met: {report['uncovered']} statements uncovered")


if __name__ == "__main__":
    main()
