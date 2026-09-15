Task: M0142-0016a — scoping recon: measure M0142-0016's blast radius before
implementing the fix. **DONE and committed** this loop. M0142-0016 itself
remains open (unstarted implementation), now with a concrete resume point.

Files: `docs/design/0100-0149/m0142-0016a-scoping-recon-blast-radius.md`
(new), `docs/design/README.md` (indexed), `.ralph/fix_plan.md` (0016's own
entry amended with an "UPDATE" block + new 0016a entry, both `[x]`/done).
No production diff — `internal/optimizer/cardinality.go` was temporarily
instrumented (env-gated `GOOPG_M0142016_TRACE` trace in both
`estimateLateralIndexJoin`'s and `estimateNLIndexJoin`'s plain-INNER
branches) then fully reverted before commit; `git diff` on it is empty.

Key symbols: `estimateLateralIndexJoin` (`cardinality.go:382-396`),
`estimateNLIndexJoin` (`:241-267`) — both twins' plain-INNER arms
(`return l` unconditionally). `joinResidualSelectivity` (`:1316-1345`) —
**ruled OUT as the fix mechanism**: its loop skips any clause where
`exprSide(c, leftWidth) != sideMixed` (`planner.go:6795`), so a
single-relation filter like Q95's `ca_state = 'VA'` (entirely on the
right/inner side) is invisible to it. The actual fix target is
`IndexScan.Cond`/`IndexOnlyScan.Cond` (`plan.go:836`/`:1022`) — the
parameterized-probe leftover-residual field neither `nliSemiMatchFraction`
nor `lateralNLIMatchFraction` reads today. This is new information not in
M0142-0016's original filing; the next loop implementing 0016 should start
from `Cond`, not from `joinResidualSelectivity`.

Findings this loop: TPC-H 3/21 queries (Q7 25, Q8 33, Q21 19 call-site hits)
carry a real defect (probe has `Cond`); 6 more queries (Q2/Q5/Q9/Q11/Q17/Q20)
hit the shape but as a no-op (no `Cond`, current code already correct).
TPC-DS SF0.25: 55/99 queries carry a real defect (Q77 427, Q49 404 highest),
88/99 hit the shape at all. Cross-validated M0142-0013's Q95 finding
independently via a DIFFERENT trace (43 real hits, matches) and confirmed
Q9's 90 hits are ALL no-ops — consistent with M0142-0013's separate verdict
that Q9's own error is PG-formula-identical, not this mechanism. Full method
(two private throwaway servers, TPC-H port 5534 / TPC-DS port 65437,
env-gated-trace-then-revert, same discipline as M0142-0012a) is in the
design doc.

Next step: implement M0142-0016 itself. Resume point: in both
`estimateLateralIndexJoin` and `estimateNLIndexJoin`'s SEMI/ANTI *and* now
plain-INNER arms, multiply by `Cond`'s own selectivity (likely via
`clauseSelectivity` or an equivalent single-clause helper — `Cond` is a
single `Expr`, not a list, so `splitAnd`+loop may be needed if it can be an
AND-tree) when the probe node is `*IndexScan`/`*IndexOnlyScan` with non-nil
`Cond`. Watch Q7/Q8/Q21 specifically for TPC-H plan-shape movement (the
recon's own risk note — DP-search ties can flip, same mechanism
M0142-0003c/M0142-0015 are chasing for Q9/Q45). Full floor-measurement suite
required on landing (TPC-H/TPC-DS plan-parity capture, `make ea-ratchet`,
SF0.25 sweep) — same treatment M0142-0006/M0142-0009/M0142-0012 got. Also
noted but NOT filed as its own task (unconfirmed on any live witness this
loop): `joinResidualSelectivity`'s SEMI/ANTI callers may have the same
single-relation-filter blind spot for cases where the residual clause is
carried in `Predicate` rather than `Cond` — worth a quick check when next in
this code, not urgent enough to file blind.

Gates run: `go build ./...` clean; `go test ./internal/optimizer/...` PASS
(2.8s, unchanged from HEAD — no production diff); `git diff
internal/optimizer/cardinality.go` empty (instrumentation fully reverted);
`make ralph-state-guard` self-repaired the same pre-existing stale
progress-marker inconsistency as last loop (not caused by this loop) before
passing. This was a measurement/doc-only recon — no TPC-H/TPC-DS parity gate
or `make ea-ratchet` applies (nothing executable changed); pgbench
pre-commit smoke still runs via the git hook on the actual commit.

In-flight: none. Both private throwaway servers (TPC-H port 5534
`/tmp/goopg-m0142016-tpch-data`, TPC-DS port 65437
`bench/tpcds/runtime_goopg/data-sf025` — the git-tracked SF0.25 gate dir,
confirmed idle before use) were stopped via `goopg stop -D …` and confirmed
gone via `ps aux`; `systemctl --user reset-failed` run on both scope names.
All scratch artefacts under `/tmp` removed. Pre-existing unrelated
uncommitted changes in the tree at loop start (`.claude/settings.json`,
`analysis/tpch-explain-baseline.md`, `ci/logs/launch.log`,
`ci/logs/scheduler.log`, plus various untracked `bench/tpcds/runtime_goopg/`
and `analysis/leftdeep-joins/` files, `.claude.json`, `.continue`,
`.opencode/`) were left untouched and are NOT part of this loop's commit —
staged this loop's own files by explicit pathspec only. Nightly triage
(`ci/logs/action-items.md`, run `20260914-235643`) was checked at loop start:
all 14 items already have open `## AI-` tasks filed under M-NIGHTLY as of
2026-09-15 — no new filing needed this loop.
