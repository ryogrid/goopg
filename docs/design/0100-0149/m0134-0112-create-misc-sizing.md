# M0134-0112 — `create_misc.sql`: sizing + postfix ISNULL/NOTNULL fix

**Status:** PARKED (`failed`, 0% parity). One contained parser fix landed; the
remaining gaps are each a multi-call-site engine feature, out of scope for a
single test-port loop.

## Oracle case

`postgres/src/test/regress/sql/create_misc.sql` (196 lines) builds a
five-level, diamond-shaped table-inheritance tree (`a_star` root; `b_star`,
`c_star` inherit from `a_star`; `d_star` inherits from *both* `b_star` and
`c_star`; `e_star` inherits from `c_star`; `f_star` inherits from `e_star`),
inserts partial rows into every table, then exercises `tbl*` wildcard
descendant-inclusive queries, `NOTNULL`/`ISNULL` postfix predicates,
`GROUP BY`/aggregate over the inheritance set, `ALTER TABLE ... RENAME
COLUMN`/`ADD COLUMN` with implicit recursion into descendants, and a TOAST
sanity check.

Sized live via `scripts/pg-regress-runner.sh -v create_misc` against the PG
18.3 oracle: 0% parity. Everything up to and including the multi-level
INSERTs and the first several inheritance-set SELECTs (through the `d_star*`
and TOAST-adjacent queries) matches byte-for-byte — the divergence starts
partway through the file, at `x.c NOTNULL`.

## Landed this loop

**Postfix `expr ISNULL` / `expr NOTNULL`** — historical PostgreSQL synonyms
for `expr IS [NOT] NULL` (`postgres/src/include/parser/kwlist.h`: both
`TYPE_FUNC_NAME_KEYWORD`; `gram.y`: `a_expr ISNULL` / `a_expr NOTNULL`
productions). goopg's keyword table (`internal/parser/token.go`,
`keywords.go`) had no `isnull`/`notnull` entries at all, so every use raised
a hard syntax error (`syntax error at or near "notnull"`) — this file uses
both forms twice each, and one of the two `WHERE ... ISNULL` predicates gates
downstream `RENAME COLUMN` queries later in the file, so the syntax error
was blocking far more of the file's row-count checks than its own two lines.

Fix: added `KwIsnull`/`KwNotnull` keyword constants (both `KwCatTypeFunc`,
matching kwlist.h's category) and a postfix-desugar arm in the expression
parser's binary-operator loop (`internal/parser/select.go`, immediately
after the existing `IS [NOT] NULL` handling) that builds the same
`*IsNullExpr` node the `IS NULL`/`IS NOT NULL` spelling produces —
`Negated` is set directly from which keyword matched, with no further
special-casing needed anywhere downstream (analyzer/planner/executor all
already handle `*IsNullExpr` uniformly).

Covered by `internal/parser/isnull_notnull_test.go`
(`TestParseIsnullNotnullPostfix`, `TestParseNotnullPostfix`,
`TestParseIsnullNotnullEquivalentToIsNull`).

Verified live against the oracle: the two `ISNULL`/`NOTNULL` syntax errors
are gone (diff 254 → 251 lines) — the file now runs to completion instead of
aborting on the first `NOTNULL` predicate, exposing three *further*,
independent gaps described below (previously masked by the syntax error).

## Why parked — three independent gaps, none contained

### (1) `ALTER TABLE ... RENAME COLUMN` never recurses into inheritance children

Already flagged in the code itself
(`internal/executor/operators_ddl.go:9898-9899`, `case
parser.AlterTableRenameColumn`): "goopg does not yet recurse RENAME COLUMN
into children." Real PG's `renameatt_internal`
(`postgres/src/backend/commands/tablecmds.c`) walks `find_inheritance_children`
and renames the same column in every descendant. Without that,
`ALTER TABLE a_star RENAME COLUMN aa TO foo` leaves `b_star`/`c_star`/…
still holding the old name `aa`; the immediately-following `SELECT class, foo
FROM a_star* x WHERE x.foo >= 2` then only sees the one row that actually has
a `foo` column (the parent's own row) instead of PG's 25-row
descendant-inclusive result — a silent row-count divergence, not an error.
The same root cause cascades into a **new, incorrect** error two statements
later: `ALTER TABLE e_star* ADD COLUMN e int4` collides with a stale,
never-renamed `e` column surviving in a descendant, raising a false
`42611 attribute "e" of relation "f_star" does not match parent's type`
(`internal/executor/operators_storage.go:1241-1244`,
`canonicalTypeClass` mismatch check — itself correct behavior for a genuine
mismatch, just firing on state RENAME COLUMN should have already fixed up).

**Resume point:** `internal/executor/operators_ddl.go`, `case
parser.AlterTableRenameColumn` (~line 9862) — add an `im.InheritanceChildren
(tbl.OID)` recursion loop that applies the same name-swap to every
descendant's `Columns` slice, mirroring `renameatt_internal`. Needs a
regression sweep afterward (RENAME COLUMN is used across many DDL tests, not
just this file).

### (2) `polygon` has zero text-output canonicalization

No `parsePolygonLiteral`/`polygonCanonicalText` pair exists anywhere (unlike
`box`/`circle`, which each have one: `internal/executor/expr.go:2262/2424`
and `:2439/2497`). Both chokepoints that canonicalize box/circle —
`internal/executor/codec.go`'s `coerceTextLikeDatum` (box/circle arms around
lines 272–296) and `expr.go`'s typed-literal cast switch (lines 4092–4111) —
have no `polygon` arm, so a polygon value's raw literal text is echoed back
unchanged instead of PG's canonical double-paren-wrapped `((x,y),(x,y))`
form. This file's `f_star* WHERE x.c ISNULL` query returns polygon values as
`(111,555),(222,666),...` (single-wrapped) where PG expects
`((111,555),(222,666),...)` (double-wrapped). Already tracked as its own
future case: **M0134-0151 — `polygon.sql`** (`not-tried`).

**Resume point:** add `parsePolygonLiteral`/`polygonCanonicalText` beside the
box/circle pair (`internal/executor/expr.go:2247-2427`) and wire both
`codec.go:272-296` and `expr.go:4092-4111` call sites — do this together with
M0134-0151 rather than as a one-off here.

### (3) `SELECT * FROM a_star*` duplicates every row 2×

`WHERE aa ISNULL` over the full `a_star*` descendant set returns 48 rows
where PG returns the canonical 24 — every single row, across every table in
the hierarchy, is doubled, not just `d_star` (which is the one table
reachable via two inheritance paths — through both `b_star` and `c_star` —
so a naive non-deduplicating DAG walk would double-count `d_star` alone, not
the whole result set). Not yet root-caused; the doubling pattern doesn't
match the diamond-inheritance topology directly, so the bug is likely in the
`tbl*` wildcard-expansion machinery itself rather than a duplicate visit of
`d_star`.

**Resume point:** trace the `FROM tbl*` wildcard-descendant-inclusion code
path (grep `InheritanceChildren`/`AccessibleInheritanceChildren` call sites
under `internal/executor`) with a throwaway `zz_probe_test.go` reproducing
this file's exact diamond shape (`a_star` → `b_star`,`c_star`; `d_star` ←
`b_star`,`c_star`) before attempting a fix.

## Ledger

`.ralph/deferral_ledger.md`, 2026-08-24, M0134-0112.
