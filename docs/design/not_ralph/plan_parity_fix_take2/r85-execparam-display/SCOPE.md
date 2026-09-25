# R85 SCOPE: render ExecParamRef via owning sublink's Args (Q41 last gap)

Lineage: R84 left Q41 at `[parameterisation]` alone —
sole-category miss program-wide. goopg renders
`(i_manufact = $0)` where PG renders
`(i_manufact = i1.i_manufact)`.

## Step-0 findings (measured, no code changed)

- D4.1 (`subplan_lower.go`) rewrites surviving correlated
  sublinks' OuterColumnRefs to ExecParamRef slots + populates
  `ParParam`/`Args` atomically per subtree (Subquery/Exists/
  In kinds; row-ctor IN, Array, MultiAssign excluded and stay
  on the OuterRows path). Flat per-statement slot space,
  like PG.
- PG's rule (`ruleutils.c get_parameter`, PARAM_EXEC arm):
  deparse the param's SOURCE expression; literal `$n`
  only when unsourced. goopg's renderer prints `$N`
  unconditionally (`operators_explain.go:1922-1926`).
  PG also forces `varprefix=true` for the source expression
  (`ruleutils.c:8711-8717`); using the inner scan's normal
  `qualify=false` would produce bare `i_manufact` and would
  NOT close Q41. The safe slice of PG's rule here is:
  substitute a mapped direct ColumnRef with forced
  qualification; otherwise keep `$N`.
- Hook site: `emitSubPlanSubtrees`
  (`operators_explain.go:583`) has both `sp.expr` (owning
  sublink) and `sp.plan` — set a `reg`-carried
  slot→Args map on entry, restore on exit (nested bodies
  shadow correctly). No tree copying (vs R65 clone
  precedent — unnecessary here).
- Current lowering produces a direct ColumnRef source or a
  forwarded ExecParamRef (`subplan_lower.go:380-395`).
  Substitute ONLY a direct non-nil `*optimizer.ColumnRef`.
  A forwarded ExecParamRef (chained correlation), or every
  other expression kind, stays `$N`. This direct-type
  allowlist avoids the incomplete legacy `WalkExprTree`
  (`unnest.go:1319-1361`) and PG's separate atomic-
  parenthesis rules for non-Var sources. Broader source
  expansion is deliberate remaining display debt.
- Build an owner map only when `len(ParParam) == len(Args)`
  and every pair has a non-negative unique slot plus a
  non-nil direct ColumnRef. Any mismatch, duplicate, nil,
  or unsupported Arg rejects the WHOLE owner map and leaves
  all of that body's slots as `$N`; never index past an Args
  slice or accept last-write-wins.
- NLI probe keys unaffected (OuterColumnRef, separate
  ancestor-namespace path — Q4 proves it renders today).
- Unmapped IDs (shouldn't happen — lowering is atomic)
  stay `$N`.

## Change

`subPlanReg` gains an active `map[int]*optimizer.ColumnRef`.
`emitSubPlanSubtrees` extracts `ParParam`/`Args` from the
owning Subquery/Exists/non-row-In expression, validates the
WHOLE mapping as above, installs it only for
`render(sp.plan, ...)`, and restores the previous map after
the body (nested bodies shadow and restore correctly). The
`ExecParamRef` arm consults the active map. It MUST NOT use
the statement-global `explainNames.bySrc` lookup first:
`SourceTableIdx` restarts at each query level and bySrc is
outermost-first, so a nested body's slot can otherwise print
the outermost relation instead of its immediate host.

For a hit, a new owner-scoped resolver walks the owning host
with a dedicated fail-closed child allowlist (not raw
`planChildren`, and never expression-hanging `NodeSubplans`).
It stops before every audited explicit scope boundary:
`Project.IsolatedScope`, `CTEScan.Child` (the CTEScan itself
may be a candidate), SetOp branches, RecursiveUnion arms,
CTEDMLPrefix plans/body, Copy.Query, and Insert/Update/Delete/
Merge children. The remaining `planChildren` arms were
audited: ordinary upper nodes, joins/NLI, bitmap trees,
Memoize, OrdinalityWrap, RowsFrom and ProjectSet do not mark
a query-scope reset and are safe to descend; leaf scan nodes
terminate naturally. The traversal returns an explicit
recognized/unknown status so an unknown future leaf cannot be
mistaken for a recognized terminal node; unknown kinds make
the whole resolution decline.

Within that owner-scope allowlist, the resolver selects
scan-like candidates exposing `source.Name`; when
`SourceTableIdx != 0`, the candidate column must carry that
same ID, while ID 0 uses a unique-name match (the aggregate-
erasure fallback). Duplicate visits are de-duplicated by node
identity/RTID. Exactly one candidate is rendered with its
RTID-keyed `bySource` name, falling back to that candidate's
truthful relation/alias base only when it has no registered
RTID. Zero or multiple candidates leave `$N` unchanged. This
proves the source belongs to the active owning level before
printing; no global bySrc fallback is permitted.

During a SubPlan body's detail render the existing walker
deliberately leaves `reg.ancestor` at the owning host; nested
bodies shadow this through the same walker lifetime. The arm
MUST NOT emit a bare source. Thus qualification is guaranteed
at the correct query level or substitution declines, matching
PG's forced host-Var qualification.
Display-only: planner, executor, costs untouched — values-
safe by construction.

Out: NLI/outer-var rendering (already correct);
PARAM_EXEC execution semantics; join-method costing;
K100; anything beyond EXPLAIN text.

## Predictions (recorded before implementing)

- P1: Q41 → MATCH (sole gap closes; no other Q41
  expression line changes — if the "different levels"
  verbose line is really about reference level, it falls
  with `$0`).
- P2: current R84 goopg captures contain PARAM_EXEC display
  at exactly TPC-H `{Q17,Q20}` and TPC-DS `{Q6,Q41}`.
  Re-census the actual pre-change capture; non-cost
  expression text may change ONLY in that resulting set,
  every changed `$N` line must match the live PG capture,
  and every other query must have zero text/shape change.
  The checked-in PG plans corroborate source expansion but
  are NOT the parity target (K9); the gate uses live PG.
  Values sweep remains clean by construction (still run as
  adjudicator).
- P3: unit pins — all three supported owner kinds;
  mapping set/restore including nested shadow; forwarded
  ExecParamRef decline; unmapped-ID `$N`; direct ColumnRef
  forced-qualified substitution; a zero/erased source ID
  resolved through the owning ancestor; an unresolvable
  direct ColumnRef retaining `$N`; a nested query-level ID
  collision resolving to the immediate owner (never the
  outermost bySrc winner); an ambiguous owner retaining `$N`;
  same-name/same-ID scans hidden below IsolatedScope and
  CTEScan.Child boundaries never selected;
  whole-owner decline on
  length mismatch, duplicate slot, nil Arg, unsupported Arg;
  nil-registry fallback.

## STOP rules

- Values MISMATCH: impossible by construction —
  stop and audit the construction, not the plans.
- Any `$N`→var substitution where PG shows `$N`:
  stop (rule over-broad). Ground-truth every current site
  against a live PG capture: TPC-H Q17/Q20 and TPC-DS
  Q6/Q41, plus any site found by the required fresh
  pre-change census.
- Q41 doesn't MATCH: re-probe (label vs text?),
  don't force.

## Gates

Units, suites, pins, TPC-H spotcheck, SF0.25 sweep
(`MISMATCH=0`), byte-guard A/B both corpora +
per-query census with ZERO EXTRA, shape-delta
alongside. `match` count is NOT a criterion
(but P1 predicts Q41 MATCH — record actual).

## Review history

- Review 1: REJECT. Blocking findings were forced source-
  Var qualification, the unsafe/incomplete generic walker,
  malformed owner-map handling, and the stale corpus probe
  list. All are addressed above by the direct-ColumnRef
  allowlist, owner-atomic validation, corrected census, and
  live-PG gate. Re-review is required before scope landing.
- Review 2: REJECT. `formatExprQual(..., true)` can still
  return a bare name when the source binding is erased, so
  forced qualification was requested but not guaranteed.
  The change now requires binding lookup, owning-ancestor
  fallback, and `$N` retention when both fail. Re-review is
  required before scope landing.
- Review 3: APPROVE. No blocking or non-blocking findings;
  reviewer verified the owning-ancestor lifetime in both
  plain EXPLAIN and ANALYZE and the nil/ambiguity-safe
  qualification fallback.
- Implementation review 1: REJECT. The binding-first lookup
  was not query-level-safe because SourceTableIdx restarts and
  `bySrc` is outermost-first; nested SubPlans could print the
  wrong relation. The algorithm above now resolves and proves
  membership inside the active owning host tree and forbids a
  global bySrc fallback. Re-review is required before changing
  the implementation further.
- Scope-amendment review 1: REJECT. Raw `planChildren` crosses
  IsolatedScope and CTE-body boundaries (and other audited
  explicit query/DML boundaries). The resolver now has an
  explicit descent allowlist and boundary stops as specified
  above. Re-review is required before implementation repair.
- Scope-amendment review 2: APPROVE. No blocking findings;
  the only note (explicit recognized/unknown traversal status)
  is incorporated above.
- Implementation review 2: REJECT. Three blockers: an RTID-
  bearing candidate incorrectly fell back to its base name when
  `bySource` registration was missing; distinct nodes with the
  same RTID were not de-duplicated; and the positive zero-
  `SourceTableIdx` path lacked a pin. All three were repaired.
  The suggested recognized-but-nonmatching and unknown-node
  fail-closed pins were added too.
- Implementation review 3: APPROVE. No blocking findings. The
  reviewer re-ran `git diff --check` and the clean optimizer /
  executor suites, and verified all previous blockers plus both
  suggested pins against this scope.
