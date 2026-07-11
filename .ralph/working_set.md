(idle — nothing in flight)

## Loop summary (2026-07-11, loop #58)

**Nightly triage:** action-items batch `20260711-011536` — all 3 AI items
(IsolationTimeouts, IsolationTuplelockUpgradeNoDeadlock,
PgWaldumpVacuumPruneRoundtrip) already `[x]` in fix_plan.md M-NIGHTLY (co-load
timing flakes / already-fixed). No new nightly work.

**Task — M0122-0004 / unimplemented_feat.json M0100-0007: MERGE
duplicate-source cardinality rule.** Verify-before-implement: entry was
medium-confidence with an `unclear` code audit ("cross-partition routing not
found"). Re-verified via 4 end-to-end probes (newDDLFixture harness) — the rule
is ALREADY fully implemented and correct across partition routing, including the
specifically-flagged cross-partition UPDATE-move case. goopg raises SQLSTATE
21000 "MERGE command cannot affect row a second time" after applying the first
mod (operators_merge.go hasDuplicate). Only real gap: PG's errhint was omitted.

Landed:
- internal/executor/operators_merge.go: added PG's errhint "Ensure that not
  more than one source row matches any one target row." at the hasDuplicate
  raise site (byte-faithful to nodeModifyTable.c ExecMergeMatched).
- internal/executor/merge_dup_source_test.go (NEW): first-ever coverage —
  TestMergeDupSource{UpdateNonPartitioned,DeleteNonPartitioned,
  UpdateWithinOnePartition,CrossPartitionMove}; asserts code/message/hint +
  first-mod-applied + cross-partition row relocation.
- unimplemented_feat.json: M0100-0007 status open→resolved (surgical Edit; cited
  proof in code_audit). json re-validated.
- docs/design/0096-0010-merge-into.md: new Follow-up section; README row updated.
- .ralph/deferral_ledger.md: resolved row appended.

Gates: go build ./... clean; go vet ./internal/executor clean; merge/upsert/
explain executor tests PASS; tpch-spotcheck PASS (Q12=2/Q13=33);
make ralph-state-guard OK (auto-repaired prev-loop completed marker).

Next loop: unimplemented_feat.json still has ~84 open entries (was 85). Bounded
candidates: parsePrimaryConninfo password (blocked on trust-only handshake),
Planner GUC stubs actual behavior, pg_index expression-index restart
persistence (hard — null-bitmap-aware decode). Avoid the interval/date hot area
(ledger rows 692-714 heavily worked 2026-07-11).

In-flight: none
