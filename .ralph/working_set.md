# Working set — M0134-0001 S21 LANDED (pushed)

**Task:** M0134-0001 (`aggregates.sql`), slice **S21 — a deferred coercion must
still carry the literal's position**. Selected per the Current Priority banner
(M-NIGHTLY drained: `ci/logs/action-items.md` still run `20260817-011734`, all 6
filed and `[x]`; nothing new to file).

**What landed:** `rank('fred') within group (order by x)` emitted its
`ERROR: invalid input syntax for type integer: "fred"` with no `LINE 1:`/`^`.
`buildAggregateCall` defers the coercion to a runtime `CastExpr`
(`planner.go:8374-8379`) and never set the type's **unexported** `pos`, leaving
0 — goopg's own convention for "suppress LINE 1". One-line fix:
`pos: argE.Pos()`, matching the sibling `PlanError` at `:8386`.

**The technique that made it cheap:** the sibling 42P21 error two lines away in
the SAME hunk already rendered its pointer lines correctly and was unchanged by
the diff — proving the plumbing worked and only one position was missing, before
any code was read. Reuse this: read the hunk's unchanged context for a
same-mechanism control.

**Twin: NONE, positively verified** (one evaluator site `expr.go:514` in
`evalExprSlot`, shared by general exprs and the hypothetical-set direct arg; one
construction site, its two callers share it). Recorded as a result because
S11/S17/S18/S20 were each bitten by an unpaired twin.

**Measurement:** `aggregates` **943 → 930 lines, 26 → 25 hunks** — scoping
prediction hit exactly. Sentinel `functional_deps` 56 unchanged.

**Files:** `internal/optimizer/planner.go` (1 line + comment),
`internal/executor/hypothetical_set_agg_errpos_test.go` (new, 2 guards),
`docs/design/0134-0001-p9-agg-coercion-error-position.md` + README row.

**Gates run:** `go build ./...` PASS; `internal/optimizer`+`internal/executor`
PASS; UNITS PASS; caret column verified byte-identical vs PG via `psql` on a
capped throwaway server; pgbench smoke PASS via hook.

**Deferral ledger:** 1 new row 2026-08-17 — the coercion still happens at the
wrong TIME (PG folds the literal at parse analysis, `parse_coerce.c:coerce_type`
:294-304; goopg evaluates per row), so a shape that never evaluates the arg
(zero rows, `WHERE false`) should error in PG and stays silent in goopg. No
corpus witness; the row carries the probe.

**Next step:** continue **M0134-0001** at 25 hunks. Best candidates: the NOTICE
trans-function ordering hunk (real `nodeAgg.c`-style execution-order gap, not
string-only); the `Group Key:` qualification-under-`Append`/inheritance pair
(hunks #8/#9, small, root cause untraced); the multi-target +
inheritance/`Merge Append` min/max bundle (ONE slice per the S19 row). Ruled
out: deparser/C11c (8 hunks, own milestone), VERBOSE `Output:` pruning,
class-10 varno, correlated-subquery class (d), and the plain-aggregate
collation successor (**zero regress-diff witness** — would not move the count).

**Delegation:** `tmp/ralph-handoffs/m0134-0001-s21-scope/report.md` (researcher,
1 round — its 26-hunk table is the map for the next slice) and
`tmp/ralph-handoffs/m0134-0001-s21-cast-errpos/report.md` (implementer, 1 round,
no deviations).

**In-flight:** none.
