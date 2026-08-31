# 0119-0006bm — `pg_class.relam` was btree for every gist/gin/spgist/brin index

**Milestone:** M0119-0006 (deferral-ledger backlog, pg_amcheck server tier)
**Date:** 2026-09-01
**Status:** accepted

## Empirical discovery

`postgres/src/bin/pg_amcheck/t/003_check.pl`'s fixture (the AC-003 blocker) was
long assumed to be gated on "unsupported index AMs" — goopg lacking hash/gist/
gin/brin/spgist physical index implementations. Driving the real `pg_amcheck`
binary against a live goopg cluster with the fixture's exact SQL (schema +
`box`/`int4range`/`int4[]` columns + a `USING {BTREE,HASH,BRIN,GIST,GIN,SPGIST}`
index on each column, per the upstream script) shows the **entire fixture setup
already succeeds** — box/int4range/int4[] types and all six `CREATE INDEX ...
USING <am>` statements are catalog-accepted today. That part of the AC-003
blocker premise is stale (mirrors the 2026-06-15 `003_check.pl` whole-DB
enumeration finding, `[[ac003_blocker3_refuted_pg_amcheck_whole_db_clean]]`).

Running the real `pg_amcheck` binary against that fixture surfaced a genuine,
narrow bug instead:

```
btree index "postgres.s1.t1_gist":
    ERROR:  only B-Tree indexes are supported as targets for verification
    LINE 1: SELECT "public".bt_index_check(index := c.oid, heapallindexe...
    DETAIL:  Relation "t1_gist" is not a B-Tree index.
```

`pg_amcheck.c`'s own relation-enumeration query (`prepare_relation_check_command`,
`pg_amcheck.c:2050`+) filters btree-checkable indexes with `c.relam =
BTREE_AM_OID` — a `USING GIST`/`GIN`/`BRIN`/`SPGIST` index should never even
reach `bt_index_check` in real PG, because its `relam` isn't the btree oid
(403). Querying goopg directly showed why it did reach it:

```sql
SELECT c.relname, c.relam, am.amname FROM pg_class c JOIN pg_am am ON am.oid=c.relam
WHERE c.relname LIKE 't1_%';
--  t1_gist | 403 | btree     -- WRONG: should be 783/gist
--  t1_gin  | 403 | btree     -- WRONG: should be 2742/gin
```

## Root cause

Two sibling pg_class row builders compute `relam` for a user index and both
only special-cased `"hash"`, defaulting every other non-btree method to
btree's oid:

- `internal/catalog/catalog.go`'s virtual `pg_class` `VirtualRows` builder
  (the live in-memory catalog every ordinary query reads).
- `internal/executor/pg18_user_catalog_rows.go`'s `buildUserPGClassRowForIndex`
  (the heap-persisted row synced for a real-PG-standby-on-goopg-catalog reader,
  `[[per_connection_virtual_catalog_scoping]]`-adjacent durability path).

`catalog.go`'s `if idx.Method == "hash"` branch was itself dead: a `USING
hash` index deliberately stores `idx.Method == "btree"` (it's physically built
on the B-tree substrate, `idx.DeclaredHash` is the separate marker —
`IndexAMCapabilityByName`'s doc comment, `internal/catalog/catalog.go:20594`),
so that string literally never appears as `idx.Method`'s value. gist/gin/
spgist/brin indexes, by contrast, ARE registered catalog-only under their
real `Method` string (`execCreateIndex`'s `method == "gist" || ... ` branch,
`internal/executor/operators_ddl.go:7632`) — the relam builders just never
consulted it, so `AccessMethodOIDByName(idx.Method)` was sitting unused one
file away (`internal/catalog/catalog.go:20475`, already the canonical name↔oid
map used by `CREATE OPERATOR CLASS`/`pg_indexam_has_property`).

## Fix

Both builders now resolve `relam` from `idx.Method` via the existing
`AccessMethodOIDByName`, carved out for `idx.DeclaredHash` (which keeps
reporting btree's oid, matching the documented "everywhere else in goopg"
hash-reports-as-btree contract — no behavior change for hash indexes, which
were already (deliberately) reporting 403):

```go
idxRelam := "403" // default btree
if !idx.DeclaredHash {
    if amOID := AccessMethodOIDByName(idx.Method); amOID != 0 {
        idxRelam = strconv.Itoa(int(amOID))
    }
}
```

and the sibling `indexRelamOID(idx *catalog.Index) int64` helper in
`pg18_user_catalog_rows.go` for the heap-persisted path.

## Verification

Fresh capped server, real `pg_amcheck` binary (not a Go simulation) against
the upstream fixture subset (btree/hash/brin/gist/gin/spgist indexes over a
`box`/`int4range`/`int4[]`/`int` table):

- Before: `pg_class.relam` reported `403` (btree) for every non-hash,
  non-btree index; `pg_amcheck --schema=s1 postgres` exited 2 with six
  `is not a B-Tree index` errors from indexes it should have silently
  excluded.
- After: `pg_class.relam` correctly reports `783`/`2742`/`4000`/`3580`
  (gist/gin/spgist/brin); the identical `pg_amcheck` run exits **0**, no
  output — the enumeration query now excludes them exactly as it does
  against real PG.

Gates: `go build ./...`; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh`; `scripts/tpch-spotcheck.sh`.

## What's still blocking `003_check.pl`/`004_verify_heapam.pl` proper

This slice removes one specific false-failure mode (the enumeration
over-match); it does **not** unblock the full `003_check.pl` fixture, whose
corruption-injection steps still need capability goopg doesn't have for
hash/gist/gin/brin/spgist indexes (they're catalog-only, no physical pages to
corrupt) and `STORAGE EXTERNAL` TOAST + multi-database orchestration for the
later sections. See `docs/test-port/postgres-oracle-target-inventory.csv` row
AC-003 for the itemized remainder; this doc only closes the relam-metadata
gap, filed as its own ledger row so it isn't re-discovered as part of a future
attempt at the bigger fixture.
