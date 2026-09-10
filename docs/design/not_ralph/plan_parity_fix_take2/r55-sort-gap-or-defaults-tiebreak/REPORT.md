# R55 REPORT — §2 OR-conjunct inequality + IN-list arms (2026-09-11)

Date: 2026-09-11. Scope: `SCOPE.md` §2 ONLY (P3 live, P1/P2 withdrawn,
§3 tie-break ledgered and ordered after the R56 `costAgg` audit).
Tree @ `80aa3f4c5` + uncommitted cut: `selectivity.go` stats-first
extraction (`rangeOpSelectivityStats`, +28) + `joinselectivity.go`
`orConjunctSelectivity` dispatch with two new arms
(`orRangeSelectivity`, `orInListSelectivity`, +131) + 4 tests
(+146). Binary: `/tmp/pp2/bin/goopg-r55` (built 01:14, newer than
all three sources — no rebuild needed). Comparator: R54 q7fix
baselines in `/tmp/pp2/r54redesign/` (NOT reland: reland is the
pre-OR-fix FAILED round, Q7 top join 229626). Same capped clones
and GUCs throughout (`/tmp/pp2/clone-tpch` + `/tmp/pp2/clone-ds05`
:5533, `work_mem=64MB`, `mpwg=4`, `GOOPG_ANALYZE_SEED=20260905`,
fresh server per phase, every query ×2 run-stable — all pairs
STABLE). Top-mode trace + serial-mode, values arm, Q12/Q13
substance clone-local (bench :65433 BUSY — peer server, never
touched; `scripts/tpch-spotcheck.sh` would stop a foreign server,
so the R54 clone-local precedent via `tpch-runner` stands in).
Evidence (tmp-only, NOT committed): `/tmp/pp2/r55/` (drivers
`run.sh`, `run-q84.sh`; server logs `start-*.log` carry the
DPPATH trace; `q{1,3,5,7,8,9,10,19}-{top,serial}-r55`,
`q84-{top,ds05}-r55`, `pp-r55.txt`, values digests).

## 1. Exit table (P3 prediction vs measurement)

| item | R55 | q7fix baseline | verdict |
|---|---|---|---|
| P3 Q19 brand-OR join | **26** (trace `producer=join.hash.partial relids={0,1} … rows=26` ×2; plan-line `Parallel Hash Join … rows=26`, cost-only: 166587.05→166585.71) | 133 | **✓ LANDS: inside the 24–94 bar, near the lower edge, direction DOWN** |
| Q19 shape | Finalize→Gather→Partial→Parallel Hash Join, Join Filter byte-identical incl. all three brand/container/quantity/size OR branches | same | **✓ shape-identical, cost-only move (4-line diff, all cost/rows)** |
| Q7 top | byte-identical plan ×2 (join rows=1468 on the line) | 1468 | **✓ pin holds** |
| Q7 serial | byte-identical plan ×2 | gathered wins by 32.82 | **✓ pin holds** |
| Q8 top+serial | byte-identical plans ×2 both modes | split wins by 161.33 | **✓ untouched (no OR clause in Q8 — arms correctly silent)** |
| Q5/Q1/Q3/Q9/Q10 | byte-identical plans (top; Q5 ×2) | — | **✓ zero-move outside the OR-join shape** |
| values 8/8 | md5 MATCH all 8 (`q1/q3/q5/q8/q9/q10` vs `*-q7fix2.md5`, `q7/q19` vs `*-q7fix.md5`, verified by direct recomparison) | — | **✓ digest carries** |
| Q12/Q13 | 2 / 34 rows, OK | canonical 2/34 | **✓ no `Q12=0/Q13=2` failure signature** |
| q84-tpch | identical ERROR control (`customer_address does not exist`) ×2 | same | **✓ control reproduces** |
| q84-ds05 | byte-identical stripped plan ×2 (Nested Loop rows=23, cost 12405) | same | **✓ DS upper-tournament representative unmoved** |
| DS SF0.5 sweep | **PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=4** (`GOOPG_BIN=/tmp/pp2/bin/goopg-r55`, foreground) | — | **✓ §4 must-hold (95 all-zero)** |
| parity-diff | Q19 SHAPE-DIFF `[join-order,join-method,scan-type,parallelism,qual-placement]`, unparsed=0 — rerun of the R54 pair alongside gives the **identical verdict line** (only delta: goopg Q19 top cost 166587.05→166585.71) | same SHAPE-DIFF (pre-existing: goopg hashes Q19, PG nested-loops it) | **✓ no regression, unparsed=0 required holds** |

P3 lands 133→26 against the 24–94 bar: inside, near the lower
edge, direction DOWN — the scope's pass criterion (a landing above
94 or at/above 133 would have FAILED it; neither happened). The
~47 PG anchor remains approximate (different join shape), so no
exact-anchor claim is made; the residual 26-vs-47 gap is the
ledgered general-ANY + cross-table shapes, NOT the const-IN-list
this cut covers (Q19's `p_container` arms are const-IN-lists —
the easy side, now measured).

## 2. What the cut does (and deliberately does not)

- `orConjunctSelectivity` dispatches BooleanConst → `*InExpr` →
  BinaryOp switch (Eq/Ne → R54 arm extracted unchanged as
  `orEqualitySelectivity`; Lt/Le/Gt/Ge → `orRangeSelectivity`
  with `swapInequalityOp` on swapped operands; anything else →
  whole-OR default as a guess). The R54 equality path is
  byte-untouched in behavior — Q7's pins prove it live.
- `orRangeSelectivity` attributes via the equality arm's
  positional `SourceTableIdx` → `relInfos[].sourceIdx`
  translation, then measures with `rangeOpSelectivityStats` —
  the MCV+histogram body moved verbatim out of
  `rangeOpSelectivity`, stats-first instead of `child`-Node
  lookup. One shape, one arithmetic, two callers (sibling-paths
  rule documented at both call sites).
- `orInListSelectivity` declines Negated/AllOp/NotEqualAny/
  `Plan`/non-ColumnRef/non-Eq-or-range-AnyOp/unattributable/
  nil-stats/any-non-const-element as (0.5, guess) — the
  general-ANY hard shapes stay guesses — else per-element
  `eqSelectivityForColumn` or `rangeOpSelectivityStats` with
  `inListSelectivity`'s OR-merge loop mirrored element for
  element (disjoint-sum accepted exactly when the restriction
  path accepts it, in [0,1]).
- Clamp semantics preserved: new arms return measured=false
  ONLY with a computed value (partially-measured ORs flow it —
  `joinrelsize.go:230-275` verified pre-implementation); every
  decline is (0.5, true), keeping the all-default clamp armed.
- q84-ds05 is the negative control that matters: DS Q84 carries
  restriction inequalities (`ib_lower_bound >= 60306`,
  `ib_upper_bound <= …`) but NO OR-join conjuncts — byte-identical
  corroborates that the arms fire only inside OR-join estimation,
  never on the restriction path (whose `rangeOpSelectivity`
  wrapper is behavior-preserving: same body, same
  `defaultIneqSelectivity` fallbacks). The primary justification
  is structural, not empirical: `rangeOpSelectivityStats` has
  exactly three call sites — the restriction wrapper
  (`selectivity.go:316`) plus the two OR arms
  (`joinselectivity.go:546,602`) — so no other estimator can
  observe the new arithmetic; q84 is one query's corroboration.

## 3. Unit pins (all green, no `-count=1`)

- `go test ./internal/optimizer/` ok (cache-valid on this tree),
  `go test ./internal/executor/` ok (13.3s), `go vet` clean.
- 4 new tests: range-arm attribution/dispatch vs the restriction
  core + swapped-operand identity (deliberately NO below-0.5
  assertion — range selectivity is data-dependent, unlike the
  structural 1/625 equality arms); range decline without
  histogram → (0.5, true); IN-list 4-element disjoint sum vs
  per-element `eqSelectivityForColumn`; general-ANY declines
  (plan/NotEqualAny/AllOp/nonconst/negated → 0.5, true).
- `gofmt -l` flags `joinselectivity_test.go:521` — verified
  pre-existing at HEAD (`git show HEAD:` version flags
  identically; newer-gofmt blank-line rule). New code adds zero
  diffs; left untouched per the go1.25 baseline rule.

## 4. Gate ledger (run vs deferred-with-rationale)

- FIX-SEED §5 must-holds: structural Q19 MATCH carries
  (shape-identical above the join, rows=26 on the join line —
  the pin's structural mode strips rows; the mode verified, not
  assumed) ✓; Q5-split-wins non-vacuous (byte-identical plan,
  split still wins — a serial win would have failed) ✓;
  TPC-H digest 8/8 MATCH ✓; DS SF0.5 PASS=95 all-zero ✓
  (foreground this round — stronger than R54's deferral).
- `pg-plan-parity-diff` on the measured corpus vs live PG
  capture: verdict identical to R54, unparsed=0 ✓ (§1 table).
  Full-corpus PG captures are not re-taken: every plan outside
  the OR-join shape is byte-identical to the q7fix corpus the
  R54 parity verdict already covers.
- `estimate-audit` full arm: NOT run — the P3 gate (133→26,
  attributed end-to-end through the OR-estimator arms, §2) IS
  the estimate audit for the touched estimator; the full arm
  needs the shared bench cluster (collision rule — :65433
  BUSY) and would re-measure untouched estimators.
- `make plan-gate` live: NOT run (needs the shared bench
  server) — substituted with file-based byte-diffs q7fix→r55
  on all 10 captures: IDENTICAL ×9 files, DIFFER Q19-only
  whose 4-line cost/rows move is the intended P3 effect (§1).
- `scripts/tpch-spotcheck.sh`: NOT run as-is (would stop the
  peer's :65433 server) — substance run clone-local via
  `tpch-runner` (R54 precedent): Q12=2/Q13=34 canonical ✓.

## 5. Exit (owed by SCOPE §2/P3)

P3 LANDS: 133→26, inside 24–94 near the lower edge, shape-identical,
attributed to the two new arms through the clamp-off chain
(measured arms flow values; declines hold the default). Q7 pins
bit-exact both modes; Q8 untouched as predicted (no OR clause);
q84-ds05 byte-identical (restriction inequalities unaffected);
values 8/8; spotcheck canonical; sweep 95 all-zero; parity
verdict unchanged with unparsed=0. Must-holds all green. The
residual (26 vs ~47 anchor, general-ANY shapes, Q8 tie-break)
is ledgered follow-up, not this round. Nothing here authorises
§3 calibration — still ordered after the R56 `costAgg` audit.
