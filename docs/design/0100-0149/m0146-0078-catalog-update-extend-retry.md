# M0146-0078 — catalog UPDATE retry must extend the relation

Status: done 2026-10-07 (`f8062512e`).

## Symptom

A script that creates or replaces nine small PL/pgSQL functions succeeded
on its first run. On every later run, the same `CREATE OR REPLACE
FUNCTION` failed with:

```
ERROR:  catalog update: freshly extended page did not accept tuple
```

Regress `plpgsql` and `polymorphism` hit the same error. In
`polymorphism`, `CREATE FUNCTION testpolym` failed, so the following
`select * from testpolym(37)` reported `function testpolym does not
exist`.

## Cause

`updateHeapRowCanonicalPG` (`operators_storage.go`) writes a catalog row's
new version without HOT. It picks the target page with `pinNewTarget`: the
relation's last block when that block is not the old version's own,
otherwise a freshly extended block.

On `ErrNoSpaceInPage` the loop retried, but `pinNewTarget` was called with
nothing changed. `NBlocks` was the same, so the retry picked the same full
last block, failed again, and raised "freshly extended page did not
accept tuple" without ever having extended the relation. A replace whose
new `pg_proc` version did not fit the last page therefore failed.

## Change

`pinNewTarget(extend bool)` forces an extension when `extend` is set, and
the retry passes `true`. This matches PG's `RelationGetBufferForTuple`
(hio.c), which extends the relation when no existing page has room.

## Verification

- `TestCatalogUpdateExtendsWhenLastPageIsFull` builds a full old-version
  page and a full last page, then updates the row. The new version must
  land on the freshly extended block 2. At HEAD the test fails with the
  production error.
- The probe script (`tmp/m72-probe.sql`) runs five times in a row with no
  error, and its output is identical to PG's. After a server restart,
  `pg_proc` holds the same 9 rows PG does.
- Regress A/B over 7 files:
  - `plpgsql` loses the error;
  - `polymorphism`'s `testpolym(37)` returns PG's row;
  - the other five files are identical.
- Gates:
  - units PASS;
  - tpch-spotcheck PASS;
  - acceptance arm 24/24;
  - sf025 96/96, plan shapes 99/99;
  - ea-ratchet PASS.

## Not covered (ledgered)

PG's `RelationGetBufferForTuple` asks the free space map for any page with
room before it extends. goopg's catalog update tries only the last block
and then extends. Catalog relations therefore grow faster than PG's under
repeated updates, though no result is wrong.
