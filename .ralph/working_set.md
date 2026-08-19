# Working set — M0134-0012 PARKED; real engine fix LANDED; next is M0134-0013

**Task:** M0134-0012 (`update.sql`). Parked like 0008-0011 — and like those, the
sizing round yielded a **real, shipped engine fix**.

**What landed.** `routeToPartitionDepth`'s `case "LIST":` arm
(`internal/executor/operators_storage.go:2934`) formatted the routing key with a
CLOSED if/else over `KindInt`/`KindString`/`KindBool`/null and **no default arm**.
A `numeric` LIST key (already coerced by `coerceRowForConstraintChecks` before
routing) therefore produced `keyStr == ""`, matched no bound, and raised
`23514 no partition of relation ... found for row`. Net effect: a LIST-partitioned
table with a numeric/float/date/uuid key could not accept **any** row. Fix = one
line: call the existing `partitionKeyDatumToListStr` helper. Design:
`docs/design/m0134-0012-list-partition-numeric-routing.md` (indexed).

**Refuted en route:** the obvious theory — that multi-value `FOR VALUES IN (2,3)`
bound storage only honoured the first value — is WRONG. Bounds are stored
per-value on both DDL paths (`operators_ddl.go:8926` ATTACH, `:4982` PARTITION OF),
matching PG's flattened `create_list_bounds`. Do not re-open it.

**Lesson worth carrying — the sibling pair's *copy* was the correct twin.**
Three sites format a partition key for the same `FindPartitionForValue` string
compare: the RANGE arm (`:2966`) and `partitionKeyDatumToListStr` (`:3211`) both
have `default: d.Format()`; only the LIST arm did not. The helper's own doc
comment said it "mirrors the LIST arm" — the mirror had drifted *ahead* of the
original. Usual `pattern_sibling_paths_must_agree` instinct, inverted: when you
find a duplicated formatter, check which copy is stale, don't assume the
canonical-looking one is right.

**Why PARKED.** Eight independent root causes. The dominant bucket (~300 of 841
lines) is multi-level partition row routing through column-reordered intermediate
partitions — REFACTOR-tier. Buckets 2 (partition constraint not enforced on
UPDATE, 9 stmts) and 3 (RLS on row movement, 4 stmts) are partially CONFOUNDED by
it: those partitions may look "silently accepting" only because they hold zero
rows. Re-arm trigger recorded on the task. CSV row stays `failed` → **no
`make regen-testport`**.

**Three deferral rows appended** (2026-08-20, M0134-0012): the nested-routing
gap; the remaining six buckets + re-arm trigger; string-vs-typed-Datum bound
comparison (`2` vs `2.0`) plus the still-suspect HASH arm.

**Next step:** select **M0134-0013 (`insert.sql`)** — a `failed` case. This loop's
gate run already measured it at HEAD+fix: **1062 diff lines, 58 `^+ERROR`, 57
`^-ERROR`**, with the visible diff tail being multi-column-range-partition
(`mcrparted`) `Partition constraint:` display via `\d+`/`pg_get_expr` — a
pretty-print gap, NOT tuple routing. Start from that, and check
`parallel_schedule` for a prerequisite before assuming the failure is real.

**Gates run:** `go build ./...` PASS; `go vet ./internal/executor/` PASS;
`go test ./internal/executor/` PASS (4 new tests in
`list_partition_numeric_routing_test.go`; FAIL-pre verified by stashing only the
`operators_storage.go` edit — 2 of the 4 failed with the verbatim `23514 no
partition of relation "list_parted" found for row`, and the negative control +
int/text/bool regression guard correctly passed both before and after, so the
guard cannot become dead code);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS (~7.5 min,
warm cache); `scripts/tpch-spotcheck.sh` PASS with **Q12=2 / Q13=35 exactly
matching `bench/tpch/spotcheck_expected.env`**;
`scripts/pg-regress-runner.sh --verbose update` 823 lines / 11 `^+ERROR` (from
841 / 13) with the reproducer row now appearing as unchanged context. Pre-commit
pgbench smoke PASS. Caveat: `insert`/`create_table` were re-run as partition-heavy
regression checks (1062 and 762 lines) but **no HEAD baseline was captured** for
them — stashing was avoided with foreign WIP in the tree, so "unchanged" is
inferred from their diff content being display-only, not measured.

**Delegation:** `tmp/ralph-handoffs/M0134-0012a` (tester, sizing, DONE),
`M0134-0012b` (researcher, root-cause + verdicts, DONE), `M0134-0012c`
(implementer, 1 round, DONE), `M0134-0012d` (tester, gates, DONE).
**In-flight:** none.
