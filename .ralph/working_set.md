Task: M0142-0010 — recon: the `date_dim+store+store_sales` join-level
cardinality gap (qerr ~42, Q34/Q73) that M0142-0009's leaf fix unmasked.
**DONE and COMMITTED** this loop (banner item 4, M0142 sub-group). Verdict:
not a new mechanism — recon-closed with NO production code change.

Files: `docs/design/0100-0149/m0142-0010-join-level-gap-is-memoize-shape-not-cardinality-bug.md`
(new, full trace+oracle-verification writeup), `docs/design/README.md`
(indexed), `.ralph/fix_plan.md` (0010 `[x]`, 0005's entry enriched with this
corpus evidence), `.ralph/deferral_ledger.md` (0010 row, names M0142-0005 as
the resume point). `internal/optimizer/cardinality.go` was temporarily
instrumented (env-gated `GOOPG_JOIN_TRACE` trace in `estimateJoin`) then
**reverted** before commit — `git diff` on it is clean.

Key symbols: `estimateJoin` (cardinality.go:608, hash/merge branch
608-699, NOT modified — verified PG-faithful), `pairNDistinct` (returns
`nd=73049` for `date_dim.d_date_sk`, a PK), PG oracle's `eqjoinsel_inner`
(`postgres/src/backend/utils/adt/selfuncs.c:2445`, the no-mutual-MCV default
branch at :2601 — `MIN(1/nd1,1/nd2)*(1-nullfrac1)*(1-nullfrac2)`, bit-for-bit
matches goopg's fallback).

Findings: instrumented `estimateJoin` on a private SF0.25 clone, reproduced
Q34's `Gather est=2111` (filed as 2105) over `store_sales`(719876)⋈
`date_dim`(235, correctly filtered) hash join, actual=93640. Traced the
formula to `l*r*(nullSel/nd)`, textbook FK->PK "uniform over referenced
domain". Measured goopg's own stats: `ss_sold_date_sk` has only 1823
distinct values (~5yr window) vs `date_dim`'s 73049-row 1900-2100 span — a
real 40x domain mismatch. Read PG's `eqjoinsel_inner`: its no-mutual-MCV
default is the SAME formula. Queried the REAL PG 18.3 oracle's own
`pg_stats` on identically-generated data: `ss_sold_date_sk` has a 100-entry
MCV, `d_date_sk` (unique PK) has NONE — so PG's own exact-overlap branch
can't fire there either, and PG's planner would compute the IDENTICAL
1.31e-5 selectivity for this exact join shape (verified bit-for-bit against
the trace). **goopg's cardinality math is PG-formula-identical here — no
defect to fix.** Real PG's actual plan is accurate only because it picks a
different SHAPE (`Nested Loop`+`Memoize`+`Index Scan using date_dim_pkey`,
pricing each probe directly instead of assuming uniformity) — a shape goopg
cannot select today for the already-filed **M0142-0005** reason (no Memoize
on the NL probe path). Conclusion: M0142-0010 is a second, independently-
verified symptom of M0142-0005, not a new mechanism. Ledger row + fix_plan
0005 entry both updated with this evidence so a future M0142-0005 pass has
it in hand.
Throwaway server/clone (port 5533, `tmp/m0142-0010/`) stopped and removed
before commit — none left running. `tmp/m0142-0009-sf025-bin` stray leftover
binary from the PRIOR loop also cleaned up this loop (42MB, unused).

In-flight: none. No server/gate process left running.

Next step: per the banner, item 4 (M0141/M0142 group) is still open.
M0142-0010 is now closed; M0142-0005 (Memoize/probe-multiplier interlock)
is the natural next pick given it now has TWO independent lines of evidence
(Q72 timeout + this loop's Q34/Q73 corpus findings) — but it is sized like
an executor slice (`nl_index_join.go:127`), not a recon, so scope it
carefully as its own task. Other open M0142 items at the same priority:
0003c (level-6 enumeration-order parity), 0004b (still-open C2 CTE-UNION-ALL
recon), 0006 (semiJoinMatchFraction NLI gap), 0007 (corr=0 index fallback,
re-measure first), 0008 (forced-rewrite-vs-search census). Also open: M0141
S2b/S3-S7 (S7 Incremental Sort blocks 14 TPC-DS queries).

Gates run: `go build ./...` clean (post-revert, nothing to build-test beyond
that since no production code changed); `make ralph-state-guard` — one
self-repair (same recurring benign stale-clean-exit-marker pattern several
prior loops have noted), clean after repair; pre-commit hook's mandatory
pgbench smoke PASS (all 3 transaction types, 0 failed). No ea-ratchet/
tpch-spotcheck/SF0.25-sweep re-run needed — this task landed zero production
diff, so the 112-entry ea-ratchet baseline M0142-0009 pinned is unchanged by
construction (confirmed: `cardinality.go` is byte-identical to before this
loop via `git diff`).
