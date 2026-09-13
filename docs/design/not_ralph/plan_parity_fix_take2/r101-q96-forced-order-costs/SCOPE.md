# R101 SCOPE — Q96 two-order PG18 cost attribution

R101 follows the validated native-PG oracle in R100 (`fecba96c3`). R96/R98
could not observe PostgreSQL's unchosen Q96 order, so they could not prove
which term actually changes the PG election. The valid private oracle now
makes a query-level measurement possible without changing either planner.

## Authorized measurement

Create two disposable, logically equivalent Q96 SQL forms that differ only in
the first two explicit INNER join orderings:

1. `(store_sales JOIN household_demographics) JOIN store`; and
2. `(store_sales JOIN store) JOIN household_demographics`.

Keep `time_dim` as the same final lateral/index-probe relation and preserve
all Q96 predicates. Record each rewritten form's complete SQL text and
SHA-256, plus the unchanged SELECT/WHERE/ORDER/LIMIT clauses. Pin
`join_collapse_limit=1`, `from_collapse_limit=1`, the R100 worker/memory
settings, and database/schema. Run each form twice on the R100 private PG
oracle for byte-identical JSON/text EXPLAIN and once for values; each value
digest/direct witness must equal 266. An EXPLAIN-shape assertion must confirm
the intended first two join subtree leaves and the final parameterized
`time_dim` probe. Capture the same forms on an isolated Goopg SF0.25 clone
under the corresponding opt-in planner controls, again with values and
repeated plans.

For each first- and second-level Hash Join, report child path rows/costs,
startup/run/total, inner-unique flag, bucket/virtual geometry evidence where
available, output rows, residual costs, and exact chosen-versus-loser margin.
The report distinguishes a cost that is merely forced by SQL order from a
normal search election; it must not call a forced order a natural PG choice.

## Boundaries

R101 is measurement only. It changes no Goopg source, cost, selectivity,
path filing, GUC default, executor, PG source, reference source cluster, or
historical artifact. Rewritten SQL lives only in `/tmp`; it is not committed
as a benchmark query. No `ANALYZE` runs. If either engine cannot preserve Q96
values or a parenthesized form does not pin its intended order, report that
fact and stop — do not add hints, disable methods, or force a Goopg order.

Before query execution: agent review, correction, `git commit -n`, and push.
After capture: stop private services, verify `git diff --check`, and commit/
push an English report. Any production cost change needs a separate reviewed
scope and full parity gates.
