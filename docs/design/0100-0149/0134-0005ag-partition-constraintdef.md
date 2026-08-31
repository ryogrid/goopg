# 0134-0005ag — `pg_get_partition_constraintdef` builtin

Status: accepted (2026-08-19)
Milestone: M0134-0005 (regress `constraints.sql` digestion)
PG oracle: PostgreSQL 18.3 under `./postgres/`

## Problem

`pg_get_partition_constraintdef(regclass) -> text` is seeded as a pg_proc row
(`internal/initdb/pg_proc_seed_data.go:2272`, OID 3408) but has **no executor
implementation**. psql's `\d+` invokes it for every partition, so `\d+` on ANY
partition fails outright:

```
ERROR:  function pg_catalog.pg_get_partition_constraintdef does not exist
```

This is regress `constraints.sql` diff hunk #13 (31 lines), but the blast radius
is far wider: partition introspection is broken for every partitioned table.

## PG semantics (oracle citations)

- `utils/adt/ruleutils.c:2096 pg_get_partition_constraintdef`
  → `partcache.c:299 get_partition_qual_relid`
  → `partcache.c:337 generate_partition_qual`
  → `partbounds.c:249 get_qual_from_partbound`
  → per-strategy `get_qual_for_list` / `get_qual_for_range` / `get_qual_for_hash`.
- Returns **SQL NULL, not an error**, when the relation is not a partition, or
  when the computed qual is empty (a lone DEFAULT partition with no siblings).
- Deparse runs with `PRETTYFLAG_INDENT` but **without** `PRETTYFLAG_PAREN`, so
  `AND_EXPR`/`OR_EXPR` add an outer paren pair *on top of* each arm's own
  parens. This is why the expected text double-parenthesizes.
- LIST/RANGE quals are prefixed with `IS NOT NULL` conjuncts over the partition
  key columns (`get_qual_for_list`, `get_qual_for_range`).
- `generate_partition_qual` recurses: a multi-level partition ANDs every
  ancestor's qual.

Ground truth for the target case
(`postgres/src/test/regress/expected/constraints.out:1367-1368`):

```
Partition of: notnull_tbl6 FOR VALUES IN (1)
Partition constraint: ((a IS NOT NULL) AND (a = 1))
```

`constraints.sql` exercises **LIST only**, single value, no DEFAULT.

## goopg state

Partition metadata is already fully **parsed** — there is no raw-text gap:

- `internal/catalog/catalog.go:504-539` — `catalog.Table.PartitionKey`,
  `PartitionMethod`, `PartitionKeyExprs`, `PartitionParentOID`.
- `internal/catalog/catalog.go:1510-1554` — `catalog.PartitionBound`
  (LIST / RANGE / HASH / DEFAULT fields).
- `internal/catalog/catalog.go:4563` — `PartitionParentOf(childOID)`.
- `internal/catalog/catalog.go:1558` — `FormatPartitionBound` renders the *bound
  spec* ("FOR VALUES IN (1)"). It is **not** the constraint qual; do not conflate
  the two.

Dispatch exemplar: `internal/executor/expr.go:9728-9779`,
`case "pg_get_constraintdef":` — same shape (regclass/OID arg, text result).

## Design

Add a `case "pg_get_partition_constraintdef":` arm to the same dispatch switch
in `internal/executor/expr.go`, resolving the OID argument exactly as
`pg_get_constraintdef` does.

### Return contract

| input | result |
|---|---|
| relation is not a partition (no `PartitionParentOID`) | SQL NULL |
| unknown/undefined OID | SQL NULL (match `pg_get_constraintdef`'s existing miss behavior) |
| LIST / RANGE / HASH partition, single level, column keys | rendered qual text |
| DEFAULT partition | SQL NULL — **deferred**, see below |
| multi-level partition (parent is itself a partition) | SQL NULL — **deferred** |
| expression-based partition key | SQL NULL — **deferred** |

Returning NULL for the deferred cases is deliberate: `\d+` then simply omits the
"Partition constraint:" line, which is a visible-but-benign divergence. Emitting
a *plausible but wrong* qual would be a silent-wrong-answer bug in constraint
introspection, which is strictly worse. Each NULL case gets a ledger row.

### Rendered forms (per strategy, single level, column keys)

Let `k1..kn` be the partition key columns, quoted with the same identifier
helper the sibling deparser uses.

- **LIST**, one value: `((k1 IS NOT NULL) AND (k1 = <v>))`
- **LIST**, multiple values:
  `((k1 IS NOT NULL) AND (k1 = ANY (ARRAY[<v1>, <v2>, ...])))`
- **LIST** including NULL: PG emits the `IS NULL` disjunct instead of the
  `IS NOT NULL` conjunct — follow `get_qual_for_list` exactly.
- **RANGE**: `IS NOT NULL` conjuncts over all key columns, then the
  lower-bound `>=` / upper-bound `<` comparisons per `get_qual_for_range`,
  with `MINVALUE`/`MAXVALUE` arms omitted.
- **HASH**: `satisfies_hash_partition(<parentoid>, <modulus>, <remainder>, k1, ...)`
  per `get_qual_for_hash`.

### Paren hazard (load-bearing)

`internal/executor/operators_ddl.go:5432 defaultExprToSQL` is the reusable
fully-parenthesizing deparser (already used by its CHECK-constraint twin
`renderCheckPredicate`). Its `*parser.BinaryOp` case self-parenthesizes but its
`*parser.IsNullExpr` case (`operators_ddl.go:5566`) does **not**. Naively
deparsing `AND(IsNullExpr, BinaryOp)` yields

```
(a IS NOT NULL AND (a = 1))     -- WRONG, one paren pair short
```

instead of PG's `((a IS NOT NULL) AND (a = 1))`. Two acceptable resolutions, in
order of preference:

1. Add self-parens to the `IsNullExpr` case — **only if** the CHECK-constraint
   and index-predicate siblings that share `defaultExprToSQL` show no expected
   output change. This is the sibling-paths rule (`CLAUDE.md` Hard-won Rule #2):
   `defaultExprToSQL` has more than one consumer, so both twins must be verified.
2. Otherwise, render the partition qual with a local builder in the new arm and
   leave `defaultExprToSQL` untouched.

## Deferrals recorded

DEFAULT-partition negation over siblings, multi-level recursion, and
expression-based partition keys are all creatable by goopg DDL today, so these
are real gaps — each gets a `.ralph/deferral_ledger.md` row with
`partbounds.c`/`partcache.c` resume points. They are out of scope here because
none is forced by `constraints.sql` and each carries independent complexity
(sibling enumeration, ancestor recursion, expression deparse).

## Gates

- `go build ./...`
- `go test ./internal/executor/ ./internal/catalog/`
- `pg-regress-runner --verbose constraints` — baseline **294 lines / 14 hunks**
  (measured 2026-08-19 at `9e43612c`); hunk #13 must disappear.
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
- pgbench smoke via the pre-commit hook (mandatory, every commit).
