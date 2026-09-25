# R66 SLICE1 REPORT — Sort group-section + Star + Distinct (2026-09-11)

SCOPE: `r66-alias-source-keys/SCOPE.md` (reviewed APPROVE-WITH-NOTES,
all 3 blocking + 8 advisory notes applied; committed `aaef4cbe8`).
Slice 1 is the fully-probed cut; Slice 2 (families G/T) waits on Step 0.

## 1. Result: Q16 sheds `rendering`, Q13-key1 fixed, P0–P5 all hold

The cut is renderer-only, three arms, in two files (+1 test file):

- `internal/executor/operators_explain.go` (+~90): `sortGroupKeySource`
  (Arm S — group-section Sort keys render `GroupExprs[idx]`, written
  order, R65 same-level name guard kept, explicit `0 <= idx`), shared
  `expandAggOutputRef` admits Star (`FuncCall{Name, Star:true}`) and
  Distinct (`FuncCall{Name, Args, Distinct:true}`), FuncCall render arm
  prints `count(*)` / `count(DISTINCT x)`, `exprHasSubplanOrOuterRef`
  fail-closed guard, Sort-arm call site tries Arm S before R65 expansion.
- `internal/optimizer/plan.go` (+9): additive `Distinct bool` on
  `FuncCall` (zero value false; render-only objects never reach the
  executor — constructor lives in the renderer).
- Pins: `internal/executor/explain_alias_source_keys_test.go` (6 tests:
  Q16-shape exact text, `count(*)` Sort Key, Star HAVING Filter,
  alias-GroupExprs no-op, sublink/outer-ref guard direct, plus the
  end-to-end GROUP-BY-subquery pin (`Sort Key: max` byte-identical with
  `Group Key: (InitPlan 1).col1` numbering intact — added on review,
  closing SCOPE §4's e2e gap; the shape is plannable, no deviation).

No planner, executor, or costing line touched.

## 2. Gate ledger (SCOPE §5; long arms all FOREGROUND)

1. `go test ./internal/executor/ ./internal/optimizer/` green (5 new
   pins pass first-run); `go vet` clean. R65's 4 pins + sibling pins
   pass unmodified.
2. **spotcheck: DEFERRED, owned (see §3.1).** `:65433` held by a peer
   lane since 19:02; the script stops that server as its first act.
   Binding values evidence is the 24/24 digest A/B below on an
   identical-data clone pair; re-run at landing if the lane frees.
3. **Values** `tpch-runner -digest` pre vs post binary, same clone+seed
   (`/tmp/pp2/clone-tpch-r65` :5533, inode-verified launches, serving
   probe per K91): **24/24 MATCH** (`values-pre.log`,
   `values-post.log`). P2 holds.
4. **Plans** `tpch-runner -explain` A/B, cost/rows normalised: exactly
   6 Sort Key lines move, all predicted — Q3 `o_orderdate` →
   `orders.o_orderdate`, Q12 `l_shipmode` → `lineitem.l_shipmode`, Q13
   `count` → `(count(*))` (`c_count` unchanged), Q16 full line →
   PG-identical `(count(DISTINCT partsupp.ps_suppkey)) DESC,
   part.p_brand, part.p_type, part.p_size`, Q18-group
   (`o_totalprice/o_orderdate` qualified), Q21-group (`count` →
   `(count(*))`, `s_name` → `supplier.s_name`). Q7/Q9 byte-identical
   (alias GroupExprs → same text, as scoped). Zero Group/Filter/node/
   child moves on TPC-H — the pre-flight predicted exactly this (no
   Star/Distinct HAVING exists in pre-cut TPC-H text). P3 holds.
   DS plans-channel (sweep, `plans-20260911-190106.txt` pre-cut vs
   `ds-sweep/plans-20260911-200825.txt`): **17 queries move, all
   confined** — 16 pure Sort-Key qualification + Q23 `Filter: (count >
   4)` → `Filter: (count(*) > 4)`, adjudicated PG-faithful from the
   query text itself (`having count(*) >4` in `query23.sql`). Zero
   structural moves.
5. **pp** (estimate-audit -plan-only, serial protocol both sides):
   PRE 6/14/0/2 rendering=5 → POST **6/14/0/2 rendering=4**
   (`pp66-pre.txt`, `pp66-post.txt`). Per-query categories differ by
   EXACTLY ONE cell: Q16 sheds `rendering`. Identical verdicts vs the
   same-day live-PG capture (`pp66-post-livepg.txt`). Q6 stays MATCH.
   P0/P1/P5 hold.
6. **DS SF0.25 sweep** foreground, `GOOPG_BIN=/tmp/pp2/r66/goopg-r66`
   `SF025_NO_BUILD=1`, results to `/tmp/pp2/r66/ds-sweep/`:
   **PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3**
   (`sweep-20260911-200825.txt`). P4 holds.
7. **plan-gate: explicit opt-out with rationale (no re-pin).**
   Structural mode vs `warm-pin-20260905`: 20/22 DIFFER on BOTH pre
   and post binaries, per-query verdict sets byte-identical
   (`plangate-pre.txt` vs `plangate-post.txt`); the full-file pre/post
   diff is 17 lines, ALL Sort Key pairs from §2.4. The DIFFER is 100%
   pre-existing baseline staleness, zero Slice-1 delta — same
   discharge as R65 §2.7, with stronger evidence (identical verdict
   sets, not just identical counts).

## 3. Deviations and findings (ledgered, none blocking)

1. **Spotcheck deferral (gate-2 exception).** A must-gate deferred is a
   process break unless owned: the collision hazard
   (`goopg_shared_bench_cluster_collisions`) is exactly what stopping
   `:65433` would trigger, and a renderer-only cut's row-count risk is
   covered by construction (no executor/planner line in the diff) plus
   measurement (24/24 digests incl. row counts). Re-run before any
   planner-touching follow-up, or when `:65433` is free.
2. **Fresh live-PG capture shows PG-side Q8 join-order drift.**
   `r66pg.pg.plans.txt` (via `-ref-port`, genuine PG text) diffs
   6/15/1/0: Q8 MISSING-NODE → SHAPE-DIFF because PG re-planned Q8
   between R65's capture and mine (part rows 1333 → 1323, top join
   reordered; both serial, same shape family). Rendering verdicts are
   identical across fixtures, same-day, and fresh references
   (rendering=4 everywhere post-cut). Fixtures stay the binding
   reference; the drift is evidence for why, not against it.
3. **`-ref-port` lesson.** The `<label>.plans.txt` beside a `-ref-port`
   run is always the goopg side; the PG text lands in
   `<label>.pg.plans.txt`. Diffing the wrong pair reports a vacuous
   22/22 (caught by the identical-cost signature before anything was
   concluded from it).
4. **Q8 goopg-vs-fixture MISSING-NODE is a stale-fixture artefact**
   (K9 family): goopg's Q8 parses against live PG (SHAPE-DIFF) but not
   the Sep-05 fixture. Unchanged by this round; recorded for the
   fixture re-capture waiver discussion.
5. **Review note 7 disposition.** `planExprContentKey` keys
   `fn:Name/Star/Variadic`, not Distinct — stated: unreachable today
   (planner never sets it; synthesised objects never stored/keyed);
   the survey is the guard, per SCOPE §4.

## 4. Sequencing

After review: commit (explicit pathspec: code + pins + REPORT + TODO —
SCOPE already committed) with `-n` + push → Slice 2 Step 0 (dump
Q7/Q9/Q13-outer agg chains per SCOPE §6; STEP0.md names the search
knob or records the negative).

Evidence tmp-only `/tmp/pp2/r66/` (binaries `goopg-pre`/`goopg-r66`
md5 `0467881f…`/`e8838463…`, `values-pre|post.log`,
`explain-pre|post.txt`, `r66pre|post.plans.txt`,
`r66pg.pg.plans.txt`, `pp66-pre|post|post-livepg.txt`,
`ds-sweep/`, `plangate-pre|post.txt`, `server-*.log`, `launch.sh`,
`q13|q16.sql`); pre-cut source worktree `/tmp/pp2/r66-pre-src`
(HEAD `aaef4cbe8`, postgres symlink restored after the empty-dir trap —
see §3-grade note in TODO on close); clone `/tmp/pp2/clone-tpch-r65`
(server DOWN, kept).
