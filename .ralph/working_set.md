(idle — nothing in flight)

## Loop summary (2026-07-11, loop #59)

**Nightly triage:** action-items batch `20260711-011536` (same as loop #58) —
all 3 AI items (IsolationTimeouts, IsolationTuplelockUpgradeNoDeadlock,
PgWaldumpVacuumPruneRoundtrip) already `[x]` in fix_plan.md M-NIGHTLY. No new
nightly work.

**Task — M0122 backlog / unimplemented_feat.json M0097-0035: pg_collation_for.**
Verify-before-implement: entry claimed "returns hardcoded 'default'" with a
STALE code_audit (cited internal/executor/expr.go:6249-6253, now interval-cast
code). Reality: pg_collation_for is a full plan-time fold (planner.foldPgCollationFor,
M0122-0005) mirroring PG's misc.c — NULL/UNKNOWNOID, explicit COLLATE, per-type
defaults, arrays, domains, 42804. Closed the one residual the code comment
flagged.

Landed:
- internal/planner/planner.go: foldPgCollationFor now takes ctx; new
  resolveContext.explicitColumnCollationName resolves a bare *ColumnRef's
  DDL-declared COLLATE from catalog.Column.Collation via the in-scope
  rangeBinding (sourceIdx identity, range fallback). Pure plan-time constant
  fold — no plan-shape/row-count impact.
- internal/planner/pg_collation_for_test.go: +4 cases (collated_tbl c_ub→
  ucs_basic, c_c→"C", c_plain→default, qualified ref).
- unimplemented_feat.json: M0097-0035 open→resolved (surgical Edit, cited proof).
- docs/design/0122-0005-pg-collation-for-array-types.md: Follow-up section.
- .ralph/deferral_ledger.md: `-` row (computed-expr collation still deferred:
  assign_expr_collations pass).

Gates: go build ./... clean; go vet ./internal/planner clean; planner suite PASS;
collation tests (planner+executor) PASS; tpch-spotcheck PASS (Q12=2/Q13=33);
make ralph-state-guard OK (auto-repaired prev-loop completed marker).

Next loop: unimplemented_feat.json now 98 resolved / 83 open. Still-deferred
computed-expression collation (assign_expr_collations) is a larger pass — see
today's ledger row. Bounded candidates unchanged from #58: Planner GUC stubs
actual behavior, parsePrimaryConninfo (blocked on trust-only handshake). Avoid
pg_index expression-index restart persistence (hard — null-bitmap decode) and
the interval/date hot area.

In-flight: none
