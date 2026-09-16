Task: M0142-0008a-3i-plumbing-c13, completed as an ANSWER-AND-REFRAME this
loop (design doc §48). c13's own filed question ("why is ctx.OuterRows
empty") is answered — it's never pushed because the crash never reaches
the lateral driver at all — but the deeper root cause (reference sharing
of a parameterized IndexScan into a plain leaf slot) is only narrowed,
not pinned. Filed follow-on **c14** (fix_plan.md) with a concrete,
different next instrumentation layer. No production code changed this
loop (a clone-before-mutate fix was tried, live-tested, DISPROVED, and
fully reverted).

Files this loop (all documentation/planning, zero production diff):
- docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md: new §48
  (§48.1 method, §48.2 both of §47.5's predictions refuted by stack
  trace, §48.3 the pointer-identity finding, §48.4 the disproved
  clone-fix attempt, §48.5 where the real defect must live, §48.6 c14's
  concrete next step).
- .ralph/fix_plan.md: c13 marked [x] (concluded — its own question
  answered, real cause re-scoped and re-filed, matching the established
  c10/c12-style convention); new task **M0142-0008a-3i-plumbing-c14**
  filed.
- .ralph/deferral_ledger.md: new row for this loop.
- internal/optimizer/{createplan,createplannl,joinsearchseam}.go,
  internal/executor/{expr,operators_join_agg,operators_nljoin}.go:
  temporary instrumentation AND a clone-before-mutate fix attempt added,
  live-tested (build private binary, private SF0.25 data copy, direct
  server start, run Q69 via psql), then FULLY REVERTED (`git checkout --`)
  before commit; `git status --porcelain -- internal/` is empty.

Key symbols this loop's instrumentation touched (all reverted): the
`expr.go:472` `*optimizer.OuterColumnRef` panic site (added
`debug.PrintStack()`, gated) — got the exact crash call stack.
`joinOp.Open` (operators_join_agg.go) — printed plan pointer/Type/Algo/
Lateral/Right-type/Right-pointer on every call. `createNestLoopPlan`
(createplannl.go) — printed at BOTH dispatch branches (NLI vs plain).
`createNestLoopIndexJoinPlan`'s `j := &Join{...}` construction site —
printed the built Join's own pointer/Lateral/inner-IndexScan pointer.
`createPlanNodeUnpriced` (createplan.go) — printed EVERY Path→Node
translation during one query run (pointer, Kind, relids, RequiredOuter
in; Node pointer/type out) — this was the instrument that finally
nailed the sharing by pointer identity, run twice with matching results.

Findings: (1) The crash's call stack (expr.go:481 → operators_index.go
:968/496/324 → join_nl_stream.go:104 → operators_join_agg.go:428 →
join_nl_stream.go:101 → operators_join_agg.go:428) proves it goes through
`join_nl_stream.go`'s `openNestedLoop` (ordinary, non-lateral,
materialize-and-replay), NEVER `join_lateral_stream.go`'s `openLateral` —
both of §47.5's predicted branches (execution-dispatch gap vs.
post-createPlan tree mutation by `nlipricesplice.go`) are wrong;
`nlipricesplice.go` never touches `*optimizer.Join` at all (grep
confirmed, only `*NestedLoopIndexJoin`). (2) `createNestLoopPlan` is
called exactly 3 times for Q69's crashing run; exactly ONE call takes the
NLI branch and correctly builds `*Join{Lateral:true}` wrapping a FRESH
(confirmed via `createIndexScanPlan`'s source: `is := &IndexScan{...}`
bare struct literal) `OuterColumnRef`-keyed `*IndexScan`. (3) A SEPARATE
`*Join{Lateral:false}` — customer_demographics paired with ONE of Q69's
three EXISTS leaves, with NO connecting predicate at all — wraps the
EXACT SAME `*IndexScan` pointer as its `Right` child, via a `PathPrebuilt`
whose `.node` field was set (at Path-construction time, before
createPlan's tree walk even starts — confirmed the ONLY site that ever
assigns the private `node` field is `newPrebuiltPath`) to that same
parameterized probe. THIS `*Join{Lateral:false}` is what's actually
executed (pointer-matched to the crash). (4) A clone-before-mutate fix
(shallow-copy `is` before `createNestLoopIndexJoinPlan`/
`createNestLoopIndexJoinPlanFused` write `.Cond`/`.Key`/`.Keys`) was
implemented and live-tested against the SAME repro: **the crash persisted
identically**, and the `PathPrebuilt` resolved to the CLONE's address —
proving the defect is REFERENCE SHARING (something else captures the
already-built parameterized probe as a reusable plain leaf), not in-place
mutation of a shared object as the file's own (now-known-wrong) comment
claimed. (5) A second, likely-independent defect is visible alongside:
the winning tree splits customer_demographics away from customer/
customer_address entirely (they end up joined to the OTHER two EXISTS
leaves later in the same tree) — a join-legality gap (pairing two relsets
with zero connecting predicate should never be legal), not merely a
lost-Lateral-flag bug.

Next step: c14 (fix_plan.md, filed this loop). Instrument
`joinsearchseam.go`'s leaf-assembly path (`extractSearchLeaves`, the
`scans`/`leaves` construction ~lines 624-717) to print, for every REAL
FROM-item entry (i < nprefix, not synthetic semiAnti leaves), the Go
pointer/type of `scans[i]` at the moment it's captured — cross-reference
against the parameterized probe's pointer to catch the exact write that
puts it into a plain per-relation leaf slot. Likely fix: a decline gate
("don't let this adapter capture a RequiredOuter!=0 path as a plain
leaf"), not a clone. Separately, check `joinIsLegal`
(joinsearchlevel.go, c9's fix target) for why a zero-predicate pairing
was ever accepted — may be the same legality-gap class, not yet extended
to this shape. Same private-binary SF0.25 method as c9-c13 (design doc
§46.1: private data-dir copy, private binary, direct start bypassing the
cgroup wrapper, temporary `joinInfoList: ctx.joinInfoList` one-liner
re-applied locally to reach the search, reverted before commit either
way).

Gates run this loop: `go build ./...` clean (after full instrumentation
+ fix revert). `go test ./internal/optimizer/...` PASS. `make
ralph-state-guard` ran and self-repaired a stale status/progress
mismatch from the prior loop's clean-exit marker (see status block). No
sweep/spotcheck needed — zero production diff this loop (pure
investigation + a disproved-and-reverted fix attempt), so the usual
planner-change gates are not applicable.

In-flight: none. Private diagnostic binary/data/logs
(`tmp/goopg-c13-bin`, `tmp/c13-sf025-data`, `tmp/c13-server.log`,
`tmp/c13-q69-*`) all stopped/removed before this write-up (`tmp/` is
gitignored regardless). All six files' temporary instrumentation and the
clone fix reverted via `git checkout --` before commit; verified empty
diff (`git status --porcelain -- internal/`).
