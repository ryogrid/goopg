"""Phase 9: rebuild unimplemented_feat.json after it was deleted from the repo
root. Deterministic reconstruction: merge items_raw again, subtract everything
in resolved_dropped.json, recompute the free resolution checks, and re-apply
the sample-audit confirmed-open verdicts. phase8 then merges agent verdicts."""
import json

from common import REPO, HERE, load
from phase5_resolve import merge_items, parse_ledger, check_ledger, check_fix_plan

items = load("items_raw.json")
merged = merge_items(items)
dropped = json.loads((HERE / "resolved_dropped.json").read_text())
dropped_feats = {d["feature"] for d in dropped}
kept = [m for m in merged if m["feature"] not in dropped_feats]
print(f"merged={len(merged)} dropped-recorded={len(dropped)} kept={len(kept)}")

ledger_rows = parse_ledger()
fix_plan_text = (REPO / ".ralph" / "fix_plan.md").read_text()

# sample-audit items previously confirmed open (from the 12-item code audit)
CONFIRMED_OPEN = {
 "MarkPageHalfDead WAL record infrastructure is implemented but producer wiring is limited to BtreeVacuum flag-trailer path; other potential producers are not yet wired":
   "LogBtreeMarkPageHalfDead has zero non-test callers",
 "Runtime enforcement of ALTER STATISTICS SET STATISTICS target during DDL operations":
   "operators_ddl.go:15258 catalog-only, not enforced",
 "ALTER FUNCTION/PROCEDURE OWNER TO / RENAME TO / SET SCHEMA enforcement (currently no-op)":
   "RENAME enforced but OWNER TO/SET SCHEMA parse-only no-ops ddl.go:6517",
 "Implement window frame clause semantics for ROWS, RANGE, GROUPS frame modes and frame exclusion options.":
   "select.go:3439 frame clauses rejected by parser",
 "Client-driven Pool/Manager/AIO hooks closure-capture optimization for activity-tracking overhead reduction, pending post-fix measurement":
   "client-driven hooks still per-call LookupCurrentGoroutine open.go:1529",
}

for item in kept:
    led = check_ledger(item, ledger_rows)
    fp = check_fix_plan(item, fix_plan_text)
    item["resolution_check"] = {"ledger": led, "fix_plan": fp,
                                "later_commits": "superseded-by-code-audit"}
    ca = CONFIRMED_OPEN.get(item["feature"])
    if ca:
        item["resolution_check"]["later_commits"] = "confirmed-open-code-audit: " + ca
        item["confidence"] = "high"
    else:
        item["confidence"] = "medium"
    item.pop("kws", None)
    item.pop("newest_i", None)

doc = {
    "generated_at": "2026-07-02",
    "source": "commit-log.json (2597 commits, 2026-04-28..2026-07-01)",
    "method": ("python prefilter + haiku vocabulary discovery + regex candidate windows "
               "+ haiku batch extraction + ledger/fix_plan/timeline resolution check "
               "+ haiku adjudication + later-title/code-grep re-verification"),
    "counts": {"raw_extracted": len(items), "merged": len(merged),
               "dropped_as_resolved": len(dropped), "unimplemented": len(kept)},
    "unimplemented_features": kept,
}
(REPO / "unimplemented_feat.json").write_text(json.dumps(doc, ensure_ascii=False, indent=1))
(HERE / "unimplemented_feat.backup.json").write_text(json.dumps(doc, ensure_ascii=False, indent=1))
print(f"rebuilt: {len(kept)} items -> repo + scratchpad backup")
