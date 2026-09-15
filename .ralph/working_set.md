Task: M0141-S2a-fix1 (banner's TOP-PRIORITY group, "M0141-S2a-fix and
M0139-0007 — costing-order unblock"). **DONE and committed this loop**
(`ca574113c`, pushed). Build/tests green, `scripts/tpch-spotcheck.sh` PASS,
`make ralph-state-guard` clean (self-repaired the usual prior-loop clean-exit
marker).

Files: `internal/optimizer/groupingpaths.go` (`aggInputWidth` gained an
`*Aggregate` param, reads `agg.InputTarget`'s kept columns when
`InputTargetKnown`), `partialaggpaths.go`/`partialaggupper.go`/
`partialsortpaths.go` (call-site updates, `nil` at the one non-Aggregate
site), `internal/optimizer/agginputwidth_test.go` (new, 2 pinning tests),
`docs/design/0100-0149/m0141-s2a-fix1-agg-input-width-preview.md` (new,
full B2 derivation + measurement), `docs/design/README.md` (+index row),
`.ralph/fix_plan.md` (M0141-S2a-fix1 checked off with results),
`analysis/m0141/m0141-s2a-fix1-{tpch,tpcds}*` (6 committed capture
artefacts).

Key symbols: `aggInputWidth` (`groupingpaths.go`, now
`(child Node, agg *Aggregate)`), `Aggregate.InputTarget`/
`InputTargetKnown` (`plan.go:1394-1395`), `stampAggregateInputTarget`
(`group_input_target.go:268`, unchanged — already ran before this task),
`narrowAggregateInput`/`pastSort` (`upper_narrow_apply.go`, the REAL
narrowing commit this preview is deliberately separate from).

Findings/measured result: TPC-H match 6 -> 8 (Q3, Q13 flip SHAPE-DIFF ->
MATCH, exactly the task's named queries; Q18 partial — drops
sort-strategy, still SHAPE-DIFF on join-order/join-method/scan-type/
aggregation-strategy). TPC-DS match floor held at 2; Q31 (inside M0141-S1's
named 32-query serial set) drops aggregation-strategy/sort-strategy/
parallelism, still SHAPE-DIFF on the rest. No category regressed in either
corpus. `shape-delta.sh` confirms exactly {Q3,Q13,Q18} / {Q31,Q78(cosmetic)}
changed plan shape. Measured on a private clone/port only — shared
:65432/:65433/:65437/:65438 clusters were read from (BASE_BACKUP clone,
live EXPLAIN) but never stopped/started/rebuilt; all private binaries/clone
dirs/cgroup scopes removed after use.

In-flight: none.

Next step: re-read the `## Current Priority` banner fresh (it may have been
rewritten since 2026-09-15's "Re-ordered" text — check the date/content
match before trusting this note). If the banner is unchanged, **M0141-S2a-fix2**
(the `hashAggEntrySize` fixed-overhead currency correction) is the natural
next pick inside the same top-priority group — it is now legitimately
attemptable, since fix1 supplies the live-at-cost-time preview R124 §7's
prior attempt lacked. Candidates to re-check once fix2 lands: Q18's residual
`aggregation-strategy` mismatch (TPC-H) and Q31's residual mismatches
(TPC-DS) — neither is proven currency-shaped rather than a different
mechanism; re-diagnose, do not assume. Also still open in this same
top-priority group: **M0139-0007a** (measure/adopt the two already-built
R108/R113 arms) and **M0139-0007b** (port Memoize's currency) from an
earlier loop's recon, if the banner ranks those ahead of fix2 for any
reason.

Gates run: `go build ./...` clean. `go test ./internal/optimizer/...` all
pass (incl. the 2 new tests). `scripts/tpch-spotcheck.sh` RESULT=PASS
(Q12=2, Q13=34). Pre-commit pgbench smoke PASS (hook-enforced, not
skippable). `make ralph-state-guard` clean after one self-repair (same
stale clean-exit-marker pattern as the last several loops — harmless,
repairs itself every time; if this recurs indefinitely it may be worth a
dedicated task to find why the marker keeps arriving stale).
