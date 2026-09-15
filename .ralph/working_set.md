Task: M0142-0003d — recon whether the earlier framing ("which cost term
underprices goopg's index-driven NLI over lineitem") was even the right
question for Q9's L6 divergence. **DONE and committed this loop
(`26f40b7ba`).** Measurement-only (private throwaway clone of TPC-H data +
scratch server; the shared `:65433` bench cluster and `:65432` PG oracle were
read-only touched or never touched at all; no production code changed).

Files: `.ralph/fix_plan.md` (M0142-0003d rewritten `[x]` with the finding;
new M0142-0003e filed `[ ]`). `.ralph/deferral_ledger.md` (new m0142-0003d
row). `docs/design/0100-0149/m0142-0003d-q9-row-estimate-collapse-not-cost-formula.md`
(new). `docs/design/README.md` (indexed). No `internal/` files touched.

Key symbols (read, not edited): `internal/optimizer/joinkeyproof.go`
(`superkeyJoinEstimate`) and `internal/optimizer/joinrelsize.go`
(`superkeyJoinSelectivity`) — the PG-shaped-DP arm's existing FK/superkey
no-fan-out mechanism, whose own header names Q9's exact
`l_partkey=ps_partkey AND l_suppkey=ps_suppkey` clause pair as its motivating
case. `internal/optimizer/cardinality.go`'s `estimateJoin` — the production
twin, not yet compared side by side.

Findings this loop (supersedes -0003c's causal story, does NOT reopen its
Finding 1): (1) -0003c's "forced-goopg-order" PG measurement
(336207.55 vs 204932.03) forced PG into a FROM-clause literal left-deep
chain that was never actually goopg's own winning Q9 shape — verified via a
fresh unforced `EXPLAIN` against a private clone: goopg's real winner is an
all-index-nested-loop chain (`part⋈partsupp` hash, then
`lineitem`/`supplier`/`orders` each NL-indexed via their PK/FK indexes,
`nation` hashed in last), never scanning `lineitem` or `orders` in full.
Forcing PG into *that* actual shape with `enable_hashjoin=off` prices it at
372230.53 — same order of magnitude, different mechanism (PG's cost model
penalizes NL-indexing through `orders`'s 1.5M rows) — so -0003c's 64% gap
survives as a real finding, just mislabeled as to which order was forced.
(2) **The decisive finding**: goopg's own DPPATH trace shows the row
estimate for `lineitem ⋈ partsupp` on the composite key is **2406**, ~2500x
below the true value (~5,999,098 — verified `partsupp_pk` is a genuine
2-column UNIQUE index on exactly `(ps_partkey, ps_suppkey)` via a live
`\d partsupp` against the shared bench cluster). Every relset containing
both relations inherits roughly this floor all the way to the top
(`{0,1,2,3}` through the full 6-way all read `rows=146`, byte-identical to
the query's own final output cardinality) — this row-estimate collapse, not
any hash/NL cost-formula constant, is what makes the all-NL-index chain look
nearly free. B8 (`indexProbeCostMultiplier`) is NOT implicated by this
finding. (3) goopg already has purpose-built code for exactly this Q9
pattern (`joinkeyproof.go`/`joinrelsize.go`'s superkey mechanism,
M0127-P5.6-f) but it evidently is not preventing the 2500x collapse at
HEAD for this relid pairing — WHY is not confirmed, deliberately left
unresolved (recon discipline: don't guess between "wrong arm is live" vs
"clause-matching precondition fails" without a unit test).

Next step: **M0142-0003e** (filed this loop) — write a targeted unit test in
`internal/optimizer/joinrelsize_test.go` or `cardinality_test.go` (NOT
another full-query trace) reproducing a bare 2-relation join of `lineitem`
and `partsupp` on the composite key with a genuine composite UNIQUE index on
the `partsupp` side, and step through `superkeyJoinSelectivity`/
`estimateJoin` to see whether either reaches/matches this case. First
re-confirm which arm (`GOOPG_PGSHAPED_DP` on/off) is actually live by
default at HEAD — don't assume, per `goopg_arm_scripts_disable_dp_search` in
memory (an arm script disabling DP search would make a real fix look like a
no-op). Do NOT edit `joinkeyproof.go`/`joinrelsize.go`/`cardinality.go`
blind before the unit test pins the exact failure mode. Other still-open
M0142/M0141 items as of this loop, none mandated over 0003e by the banner
(still item 4): **M0142-0005** (per-worker Memoize cache, large, needs its
own scoping recon), **M0142-0008a/0008b** (SEMI/ANTI decorrelation
scoping), **M0142-0016c** (Q33/Q54/Q56 shape check), **M0141-S2b/S3-S7**
(upper-planner ordering, Incremental Sort).

Gates run: `git status --porcelain -- internal/` empty before AND after this
loop's work. `go build ./...` clean (built the unmodified HEAD binary to a
private path for the scratch server; no source edited so no separate build
check was needed post-work). `make ralph-state-guard`: same pre-existing
stale progress-marker inconsistency as the last several loops (status=running
vs a stale progress=completed marker from a prior loop's clean exit),
self-repaired to in_progress, then passed clean. Pre-commit pgbench smoke
gate: ran at commit time, PASS (TPC-B ~44 tps, simple-update ~44 tps,
select-only ~149 tps, 0 failed across all three). Practice-card row-count
gate suite not required (no production code touched, same reasoning as
-0003c/-0005/-0008).

In-flight: none. Private scratch server (`GOOPG_CG_UNIT=m0142-0003d`, port
5533) stopped cleanly via `goopg stop -D`; port confirmed free. The 2GB data
clone at `/tmp/m0142-0003d/data` was deleted after use; small evidence files
(`server.log`, `*.plan`, `*.txt`) kept under `/tmp/m0142-0003d/` per the
project's existing tmp-evidence convention (referenced from the design doc,
not committed — `tmp/` is git-ignored and this is outside the repo tree
entirely so it isn't even in `tmp/`). The shared TPC-H bench cluster
(`:65433`) was never restarted, stopped, or written to — only a single
read-only `\d partsupp` / `pg_indexes` query. Nightly CI batch
(`ci/logs/action-items.md`) mtime unchanged since last loop's triage
(2026-09-16 04:45) — no new run landed, so triage was correctly skipped this
loop per the working-set instruction; the next loop should re-check mtime
before assuming it's still current.
