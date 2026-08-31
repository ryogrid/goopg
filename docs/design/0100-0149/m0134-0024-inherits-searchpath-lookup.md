# M0134-0024 — INHERITS parent lookup must honour `search_path`

Status: **LANDED** (2026-08-20). Case `generated_virtual.sql` remains `failed`
(this fixes one of its buckets, not the whole case).

## Problem

`CREATE TABLE child () INHERITS (parent)` and `ALTER TABLE child INHERIT parent`
resolved an unqualified `parent` through the **raw** catalog lookup, which knows
only the current database OID and has no notion of the session `search_path`.
Consequence: inheritance worked only when the parent happened to live in
`public`. In any other schema the statement failed with

```
ERROR:  relation "<parent>" does not exist
```

*even though `SELECT * FROM <parent>` in the very same session succeeded* — the
SELECT path is search_path-aware, the DDL path was not. That asymmetry is the
whole bug.

### Live reproduction (goopg on 127.0.0.1:5533, real PG 18 psql)

```sql
CREATE SCHEMA q3plain;
SET search_path = q3plain;
CREATE TABLE plain1 (a int PRIMARY KEY, b int);
SELECT * FROM plain1;                        -- works
CREATE TABLE plain1_1 () INHERITS (plain1);  -- ERROR: relation "plain1" does not exist
```

Two controls isolate it precisely: `INHERITS (q3plain.plain1)` (schema-qualified)
succeeds under the same search_path, and the identical unqualified statement in
`public` succeeds. So the defect is exactly *"unqualified INHERITS-parent lookup
outside `public`"*.

**It is not a generated-column bug.** It was found while sizing
`generated_virtual.sql`, whose 37× `relation "gtestN" does not exist` cascade it
drives — that file runs entirely under `SET search_path = generated_virtual_tests`,
a non-public schema, so essentially every INHERITS statement in it hit this. But
the reproduction above uses a plain table with no generated columns at all: this
is an engine-wide DDL resolution bug that the regress case merely exposed.

## PG oracle

`postgres/src/backend/commands/tablecmds.c:868` — `DefineRelation`'s
INHERITS-parent loop resolves each parent with
`RangeVarGetRelid(rv, parentLockmode, false)`, PG's standard search_path-aware
relation resolver, the same one every other unqualified relation reference in PG
goes through. goopg's raw `LookupTable` call was the odd one out.

## Fix

goopg already had the right helper — `(o *ddlOp) lookupTableWithSearch`
(`internal/executor/operators_ddl.go:25478-25489`, added by M0097-0022 for
`LOCK TABLE`). It was simply never wired to the INHERITS sites. The fix reuses it
rather than adding a second implementation:

| site | statement | change |
|---|---|---|
| `internal/executor/operators_ddl.go:1931` | `CREATE TABLE ... INHERITS (p)` | raw `Catalog.LookupTable(parentName, NamespaceDBOid(...))` → `o.lookupTableWithSearch(parentName)` |
| `internal/executor/operators_ddl.go:9803` | `ALTER TABLE ... INHERIT p` | same swap for `act.InheritParent` |

**Qualified names are unaffected**: `lookupTableWithSearch` attempts the raw
lookup first, so a schema-qualified name resolves (or fails) there and never
enters the `name.Schema == ""` fallback loop. This was verified live, not assumed
— it is the regression that a naive "always walk search_path" rewrite would have
introduced.

### Why both sites had to move together

`ALTER TABLE ... INHERIT` is the same statement family reached by a different
grammar path; `generated_virtual.sql:167` (`ALTER TABLE gtestxx_4 INHERIT gtest1;`)
exercises it directly. Shipping only the CREATE side would have left a smaller
residual divergence — not a regression, but a knowingly half-done fix.

## Verification

`TestInheritsUnqualifiedParentHonoursSearchPath`
(`internal/executor/operators_ddl_inherits_search_path_test.go`) covers four
behaviors in one function: unqualified INHERITS outside `public` (the fix),
qualified INHERITS (regression guard), unqualified INHERITS in `public`
(regression guard), and `ALTER TABLE ... INHERIT` unqualified outside `public`.

FAIL-pre was demonstrated by stashing only the production file:
`42P01: relation "plain1" does not exist`.

Critically the test does **not** stop at "the DDL no longer errors". It asserts
the child's columns match the parent's by name and order, then inserts into the
child and asserts the row is visible through a SELECT on the *parent*. A DDL that
succeeds while producing a column-less or unlinked child would be a worse bug
than the one being fixed, and only an end-to-end assertion can tell the two apart.

## Deliberately NOT fixed — the sibling inventory

The same raw-lookup pattern appears at ~25 other DDL sites. They are inventoried
in the deferral ledger (2026-08-20, M0134-0024) rather than fixed here, because
each needs its own regression guard and the blast radius of a blind sweep across
all of them is far larger than this slice. They split into two classes:

- **Hard `42P01` class** — the statement fails outright outside `public`:
  `CREATE TABLE ... (LIKE src)` (:2056), `ALTER TABLE ... NO INHERIT` (:9894, the
  direct twin of the site fixed here), CREATE INDEX (:7149), TRUNCATE
  (:15287/15305), DROP VIEW/MATVIEW (:6245/:6320), REFRESH MATVIEW (:19024),
  CREATE/DROP TRIGGER/POLICY/RULE, `ADD CONSTRAINT ... REFERENCES` (:8586).
- **Silent-degradation class** — the miss is swallowed by `if ok { ... }` and a
  guarantee is lost with no visible error: ATTACH/DETACH PARTITION
  (:8907/:8920/:9168), identity-sequence heap sync (:18564), ALTER SEQUENCE
  intra-session lock (:18707), GRANT/REVOKE lock bookkeeping (:20609).

The second class is the more insidious of the two and should be prioritised over
the first when this is resumed, despite the first being more visible.

## Related

- `docs/design/m0134-0023-txn-drop-recreate-name-reuse.md` — previous M0134 slice.
- Bucket 1 of the `generated_virtual.sql` sizing (implicit INSERT/UPDATE target
  list wrongly excludes every `GeneratedAlways` column) is the next slice for
  this case; see the deferral ledger row for its two-sites-must-move-atomically
  constraint and the non-trailing-generated-column positional landmine.
