# M0134-0090: `pg_indexam_has_property` / `pg_index_has_property` / `pg_index_column_has_property`

Status: accepted (case still `failed`, blocked on an unrelated geometry gap — see Remaining below)

## Problem

`amutils.sql` (regress-sql, `not-tried`) exercises three PostgreSQL SQL-level
functions that report index access-method capability flags:
`pg_indexam_has_property(am_oid, propname)`,
`pg_index_has_property(index_oid, propname)`, and
`pg_index_column_has_property(index_oid, colno, propname)`
(`postgres/src/backend/utils/adt/amutils.c: indexam_property`). All three were
seeded in goopg's `pg_proc` catalog (`internal/initdb/pg_proc_seed_data.go`,
OIDs 636-638) with correct arg/return types, but had zero dispatch-side
implementation — calling any of them raised `42883 function ... does not
exist`, and the file's very first query onward diverged (247-line diff).

## Root cause

goopg has no pluggable index-AM framework — a single physical index layout
backs every declared access method (`catalog.Index.DeclaredHash`'s doc
comment: a `USING hash` index is still built on the B-tree substrate). Real
PG's `indexam_property` reads its answers off a live `IndexAmRoutine` struct
each AM handler populates (`amcanorder`, `amcanunique`, `amcaninclude`,
`amgettuple`/`amgetbitmap` non-null, etc.) — goopg has no such struct to
read, so this capability surface didn't exist at all, not even partially.

## Fix

1. **`catalog.IndexAMCapability`** (`internal/catalog/catalog.go`, next to
   `AccessMethodOIDByName`) — a hand-curated static table transcribing each
   of the 6 in-tree AMs' `amroutine` literal-init flags (`amcanorder`,
   `amcanunique`, `amcanmulticol`, `amcaninclude`, `amcanbackward`,
   `amclusterable`, `amcanorderbyop`, `amsearcharray`, `amsearchnulls`,
   `amgettuple`/`amgetbitmap` non-null, `amcanreturn`) 1:1 from
   `postgres/src/backend/access/{nbtree,hash,gist,gin,spgist,brin}/*.c`.
   `AccessMethodNameByOID` is the OID→name reverse of the existing
   `AccessMethodOIDByName`.
2. **`internal/executor/amutils.go`** — reproduces `indexam_property`'s
   three-tier switch (AM-level / index-level / column-level) against that
   table:
   - AM-level (`pg_indexam_has_property`): `can_order`/`can_unique`/
     `can_multi_col`/`can_exclude`/`can_include`.
   - Index-level (`pg_index_has_property`): `clusterable`/`index_scan`/
     `bitmap_scan`/`backward_scan`.
   - Column-level (`pg_index_column_has_property`): `asc`/`desc`/
     `nulls_first`/`nulls_last` (from `catalog.Index.ColDescending`/
     `ColNullsFirst`, mirroring `pg_index.indoption`), `orderable`,
     `search_array`, `search_nulls`, plus `distance_orderable`/`returnable`.
   A `DeclaredHash` index answers as PG's real hash AM would (not "btree")
   — `indexAMNameForCapabilityLookup` swaps the lookup key, since these
   functions are specifically a compatibility-surface probe
   (`amutils.sql` explicitly queries `hash_i4_index`).
3. Three new `case` arms in `evalFuncCall`'s builtin-function switch
   (`internal/executor/expr.go`), following the `pg_get_indexdef`/
   `obj_description` pattern: coerce the oid/int args, resolve via
   `catalog.InMemory.LookupIndexByOID`, delegate to the helpers above.
4. **Test-harness gap, not an engine gap**: `scripts/pg-regress-runner.sh`
   only ran `test_setup.sql` + `create_index`/`create_view`/`create_misc`/
   `create_aggregate` as prerequisites before a named test. `amutils.sql`'s
   own header names different prerequisites (`geometry`, `create_index_spgist`,
   `hash_index`, `brin` — `parallel_schedule:79`) for the indexes it reads
   (`gcircleind`, `sp_radix_ind`/`sp_quad_ind`, `hash_i4_index`, `brinidx`).
   Without them the "expected" 247-line diff was partly an artifact of the
   runner never creating those indexes at all — not a goopg divergence. Added
   a gated prerequisite block (only when `amutils` itself is requested, to
   avoid slowing every other single-test run) mirroring the existing
   `is_named_test` pattern.

## Verified

Diff against the live PG 18.3 oracle (`scripts/pg-regress-runner.sh --verbose
amutils`) went from 247 lines (0% match, function-not-exist from line 1) to
87 lines. Every AM-level/index-level property, and every column-level
property except `distance_orderable`/`returnable` for `gist`/`spgist`
opclasses, now match the oracle byte-for-byte across all 6 built-in AMs
(btree/hash/gist/gin/spgist/brin). `internal/executor/amutils_test.go`
(`TestIndexAMHasPropertyFunctions`) covers the engine fix end-to-end
independent of the regress fixture (btree vs hash AM-level/index-level/
column-level answers, `indoption` DESC/NULLS FIRST bits, unknown-propname
NULL, `attno<=0` rejection).

## Remaining (deferred, see `.ralph/deferral_ledger.md` M0134-0090)

1. **`gist`/`spgist` per-opclass `DISTANCE_ORDERABLE`/`RETURNABLE`.** Real
   PG's `gist`/`spgist` AMs install a custom `amproperty` callback
   (`gistproperty`/`spgistproperty`) overriding these two properties *per
   opclass* (e.g. spgist's point-KNN opclass answers differently than its
   text-radix opclass — confirmed live: `sp_quad_ind` (point) has
   `distance_orderable=t`, `sp_radix_ind` (text) has `distance_orderable=f`,
   both `returnable=t`). goopg tracks no per-opclass ORDER BY/fetch-support
   registry for *built-in* opclasses (only for user `CREATE OPERATOR CLASS`
   registrations via `RegisterAmOpMember`/`RegisterAmProcMember`), so
   `indexAMColumnDistanceOrderable`/`indexAMColumnReturnable`
   (`internal/executor/amutils.go`) approximate with a column-base-type
   heuristic (`point` type ⇒ KNN-capable) that matches every case
   `amutils.sql` exercises but is not a general per-opclass answer.
2. **`geometry.sql` blocks `gcircleind` from ever being created**, which is
   the file's one remaining blocker for a full pass. `circle_tbl`'s
   dependency chain (`point_tbl` and friends) fails early on unrelated
   parser/lexer gaps (`?` prefix operator, `#` operator, `@` operator lex
   errors), so `CREATE INDEX gcircleind ON circle_tbl USING gist (f1)`
   (`geometry.sql:503`) never runs — `gist`'s AM-level/index-level/
   column-level rows in `amutils.sql` are NULL across the board where PG
   reports real values. This is squarely the point/box/circle operator
   parsing milestone (own scope, not touched here) — deferred, not
   attempted this loop.

## Files

- `internal/catalog/catalog.go` — `IndexAMCapability`, `indexAMCapabilities`,
  `IndexAMCapabilityByName`, `AccessMethodNameByOID`.
- `internal/executor/amutils.go` (new) — property-resolution helpers.
- `internal/executor/expr.go` — 3 new `case` arms in `evalFuncCall`.
- `internal/executor/amutils_test.go` (new) — `TestIndexAMHasPropertyFunctions`.
- `scripts/pg-regress-runner.sh` — gated `amutils.sql` prerequisite block.
- `docs/test-port/postgres-oracle-target-inventory.csv` — amutils.sql row
  `not-tried` → `failed` (genuinely still failing, sized live).
