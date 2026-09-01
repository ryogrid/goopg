# M0134-0186 — `without_overlaps.sql`: sizing (PARKED, no contained fix)

Status: **PARKED** (case sized live for the first time; every root cause
traces back to a single absent SQL:2011 subsystem — temporal
`PRIMARY KEY`/`UNIQUE`/`FOREIGN KEY` constraints via `WITHOUT OVERLAPS` and
`PERIOD` — and that subsystem's own two prerequisites are themselves already
REFACTOR-tier gaps ledgered under other tasks, so no independent slice was
found worth shipping this loop).

## What the file tests

`postgres/src/test/regress/sql/without_overlaps.sql` (2024 lines) exercises
PostgreSQL 18's SQL:2011 temporal-table support end to end: parsing
`PRIMARY KEY (..., col WITHOUT OVERLAPS)` / `UNIQUE (..., col WITHOUT
OVERLAPS)` on range- and multirange-typed columns (backed internally by a
GiST exclusion constraint: an equality check on the leading scalar columns
plus an overlap check `&&` on the range column), the same clause via `ALTER
TABLE ... ADD CONSTRAINT`, inheritance/`LIKE`/partitioning interaction,
insert/update conflict detection (including `ON CONFLICT`), `ALTER TABLE
... REPLICA IDENTITY USING INDEX` on such a constraint, and (in the file's
back half) temporal foreign keys — `FOREIGN KEY (col, PERIOD ref_col)
REFERENCES ...` with `ON DELETE`/`ON UPDATE` actions — including partitioned
referencing/referenced tables.

## Sizing (this loop, 2026-09-01)

`scripts/pg-regress-runner.sh -v without_overlaps`: **0/1 PASS, 3572 diff
lines, 445 `^+ERROR`, 0% parity** (first live run — CSV was `not-tried`;
the case was already policy-excluded from the default suite as "out of
scope for goopg v0" via `regressExcluded`, which is unchanged by this
sizing).

### Root-cause bucketing (confirmed via live repro, not diff inspection alone)

1. **`PRIMARY KEY (... WITHOUT OVERLAPS)` does not parse at all.**
   `pk_cols` (`grammar/goopg_ext.y:514-515`) has no `WITHOUT OVERLAPS`
   alternative — every one of the file's `PRIMARY KEY` constructions is a
   straight `syntax error at or near "WITHOUT"` (dominant bucket, ~120+
   occurrences counting cascades). `uq_cols` (`:517-522`) *does* have an
   alternative, but it is a documented placeholder that treats `WITHOUT`
   and `OVERLAPS` as two more literal column names (`"legacy's column-list
   loop takes the two keywords as two more COLUMN NAMES. Reproduced as-is;
   the AST is the contract."`) — it exists only to pin a parser golden AST
   shape (`internal/parser/testdata/parity_goldens.txt:676`), not to
   provide real semantics; feeding it through DDL builds a bogus 3-column
   unique constraint over `(valid_at, without, overlaps)`, which is wrong
   in a different way, not closer to correct.
2. **`FOREIGN KEY (col, PERIOD ref_col)` does not parse at all** — the
   `PERIOD` keyword inside a FK column list is unhandled by the FK-columns
   grammar production, so every temporal-FK statement in the file's back
   half is `syntax error at or near "<col>"` (the column after `PERIOD`).
   This is a second, independent grammar gap from (1) — PK/UNIQUE and FK
   use separate column-list productions.
3. **No GiST-backed exclusion-constraint execution path for a temporal
   key even if it parsed.** Attempting `UNIQUE (id, valid_at WITHOUT
   OVERLAPS)` on a `daterange`/`int4range` column today falls through to
   goopg's ordinary implicit-unique-index build, which raises `btree v0
   only supports int4 / numeric keys, got "daterange"` — there is no GiST
   access method, so even a correctly-parsed temporal key has nowhere to
   go. This is the same already-known "non-btree-sortable unique/PK
   column" limitation hit by several other M0134 cases (`domain.sql`
   M0134-0067's ledger row, `mvcc.sql`, others).
4. **No range/multirange operator family** (`@>`, `<@`, `&&`, `+`, `-`,
   `lower()`/`upper()`, ...) — surfaces here as `operator @> requires box
   operands` (range-contains-element, e.g. `valid_at @> '2018-01-15'::date`
   in the file's UPDATE...WHERE clauses) and `operator + requires numeric
   operands` (range union via `EXCLUDED.id + '[2,3)'` in the `ON CONFLICT
   DO UPDATE` blocks). **Already fully ledgered** under M0134-0173
   (2026-08-29 row: "goopg still has NO native range Datum and therefore no
   range OPERATOR family at all... introduce a `KindRange` Datum... port
   `range_contains_elem`/`range_overlaps`/...") — confirmed live here that
   the gap is exactly as described: `evalBinaryOp`'s `OpContains`/
   `OpContainedBy`/`OpOverlap` case (`internal/executor/expr.go:2011-2054`)
   dispatches purely on *textual shape* (array-literal `{...}` vs.
   `parseBoxText`), with no static-type awareness, so a range operand falls
   through to the box branch and fails box parsing. Retrofitting range
   support onto this text-shape dispatcher without a real typed `Datum`
   risks corrupting the (already-passing) box-operator behavior — exactly
   why M0134-0173 scoped a proper `KindRange` Datum rather than a text-shape
   patch, and why that fix was not attempted again here.
5. **Everything else in the diff is a downstream cascade of (1)-(4)**:
   once a `CREATE TABLE ... PRIMARY KEY (... WITHOUT OVERLAPS)` fails to
   parse, every later statement referencing that table reports `relation
   does not exist` / `current transaction is aborted` / (for the
   partitioned-FK block) `relation "temporal_partitioned_fk_..." does not
   exist` — none of these are independent bugs.

### What this means for scoping

Unlike several other recently-PARKED M0134 cases (`type_sanity.sql`
M0134-0182, `typed_table.sql` M0134-0183), which had one dominant
REFACTOR-tier bucket *alongside* a genuinely independent, low-risk,
already-scoped fix that shipped the same loop, `without_overlaps.sql` has
**no such independent slice**: buckets (1) and (2) are new grammar surface
specific to this SQL:2011 feature (not reusable elsewhere), bucket (3)
needs a GiST access method (a whole index-AM subsystem), and bucket (4) is
already fully diagnosed and ledgered under M0134-0173 as its own
REFACTOR-tier item with an explicit reason not to patch it narrowly. Adding
grammar support for `WITHOUT OVERLAPS`/`PERIOD` alone (buckets 1-2) would
not move a single line of this file's diff to parity, since the very next
statement in every block needs bucket (3) or (4) to actually execute —
there is no "grammar-only" win here the way `vacuum_parallel.sql`
(M0134-0185) had.

This is REFACTOR-tier and multi-subsystem (parser + GiST AM + range Datum),
well beyond a single loop. `regressExcluded`'s existing "out of scope for
goopg v0" policy note for this file is confirmed accurate and left
unchanged.

## Resume points

- Prerequisite 1 (bucket 4, range/multirange operator family): resume point
  already on file at M0134-0173's ledger row — `internal/executor/datum.go`
  (`KindRange`), `internal/executor/rangetypes.go` (already has range
  text I/O — `rangeParse`/`rangeMakeText`/`rangeBoundIn` — reusable for the
  new operators), `internal/executor/expr.go` `evalBinaryOp`'s
  `OpContains`/`OpContainedBy`/`OpOverlap` case.
- Prerequisite 2 (bucket 3, GiST access method): no ledger row yet exists
  specifically for "GiST as a general index AM" (as opposed to the
  per-feature symptom "btree v0 only supports int4/numeric keys" cited in
  several other cases' rationale) — a future loop scoping GiST should search
  for that symptom string across the ledger first.
- Bucket 1/2 (grammar): `grammar/goopg_ext.y` `pk_cols`/`uq_cols` (PK/UNIQUE
  `WITHOUT OVERLAPS`) and the FK column-list production (`PERIOD`) — only
  worth landing once (3) and (4) exist, so a `WITHOUT OVERLAPS` key can
  actually execute correctly instead of just parsing.
- Re-arm M0134-0186 after prerequisites 1 and 2 both land (a temporal
  PK/UNIQUE milestone would need both regardless of which lands first).

## Gates run

- `scripts/pg-regress-runner.sh -v without_overlaps` (sizing run; no code
  changed this loop, so no build/unit gates were needed beyond the runner
  itself completing cleanly).
- `make ralph-state-guard` PASS.
