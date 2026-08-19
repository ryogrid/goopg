# Working set — M0134-0016 PARKED; errposition fix LANDED; next is M0134-0017

**Task:** M0134-0016 (`create_table.sql`). Commit `56f17b13` (+ bookkeeping).
Design: `docs/design/m0134-0016-createtable-errposition.md` (indexed).

**Sizing.** Still FAILS at HEAD (not a stale status) — 762 diff lines /
17 `^+ERROR`, seven independent root causes. Shipped the one contained
highest-leverage bucket.

**The fix (bucket A, ~64% of the missing lines).** PG annotates most CREATE
TABLE validation errors with an `errposition` — wire field `P`, rendered by psql
as `LINE n:`/`^`. goopg emitted none, with **zero message-text mismatches
underneath**: the messages were already byte-correct, only the position was
absent. Root cause was a **sentinel collision**, not a missing feature —
`ExecError.Pos` is 0-based with `0` doubling as "unset"
(`internal/postmaster/copy.go:854-858`), while `validatePartitionKey`
(`operators_ddl_partition.go:144`) and `validatePartitionChildBounds` (`:346`)
each computed ONE position from the statement (`s.Pos()`) and stamped it on
every error. A regress statement starts with `CREATE` at offset 0, so the
position WAS the sentinel and was dropped silently; surviving, it would have
pointed at `CREATE`. PG threads a per-node `location` into `parser_errposition`
(`parse_expr.c:585-601`).

Errors now carry the offending sub-node's own `.Pos()`. `validatePartBoundExpr`'s
aggregate arm swapped its `containsColumnRef` bool probe for a real recursive
call — preserves PG's priority (column ref in the args outranks the aggregate
error) AND yields that ref's position (caret on `a` in `sum(a)`, not `sum`).
`PARTITION BY` errors had no node (parser unwraps each key's `ColumnRef` to a
bare string and discards it), so `PartitionByClause` gained `MethodPos`/
`KeyColPos` — the PG-faithful shape, matching upstream `PartitionSpec.location`/
`PartitionElem.location`.

**Faithfulness discipline that mattered:** positions added ONLY where PG's
expected output shows a `LINE`/`^` pair, verified case by case; three errors keep
`Pos: 0` deliberately. Line NUMBERS are never computed — the field is a byte
offset, psql derives `LINE n`. Guard tests assert the position EQUALS the token's
byte offset, not merely non-zero (a non-zero assertion misses a wrong caret).

**Result: 762 -> 610 diff lines, `-LINE` 57 -> 29, `+ERROR` unchanged at 17.**
CSV row stays `failed`, **no `make regen-testport`**.

**Three deferral rows appended** (2026-08-20, M0134-0016): errposition still
missing on the temp-schema pair; missing on `validatePartitionChildBounds`'s
CLAUSE-level errors (no offending expr node — needs parser positions for
`FOR VALUES`/`WITH (MODULUS ...)`/`DEFAULT` tokens); and the six unshipped
buckets (B MINVALUE overlap miss, C "No partition constraint", D `DROP DOMAIN
CASCADE` via column domain type, E DETAIL-text gaps, F index deparse parens,
G row-typed list-partition pruning).

**Next step:** select **M0134-0017 (`hash_index.sql`)** — a plain `failed` case.
Apply the stale-status rule first: `scripts/pg-regress-runner.sh --verbose
hash_index` at HEAD before designing. (Re-check the fix_plan banner first; it is
the sole ordering authority.)

**Gates run:** `go build ./...`, `go vet`, `go test
./internal/{executor,parser,optimizer}/` PASS. `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS (8m19s; `cmd/goopg` + `internal/initdb`
ran cache-cold — not a regression signal). `scripts/tpch-spotcheck.sh` PASS with
**Q12=2 / Q13=35 exactly**. Pre-commit pgbench smoke PASS.

**Delegation:** `tmp/ralph-handoffs/M0134-0016a` (researcher, sizing, 1 round,
DONE), `M0134-0016b` (implementer, bucket A, 1 round, DONE), `M0134-0016c`
(tester, gates, 1 round, DONE).
**In-flight:** none.
