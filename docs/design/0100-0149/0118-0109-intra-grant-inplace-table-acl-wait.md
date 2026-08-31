# 0118-0109 — `intra-grant-inplace` enabler: ALTER TABLE ADD PRIMARY KEY waits on a concurrent `GRANT … ON <table>`

Status: accepted
Spec: `postgres/src/test/isolation/specs/intra-grant-inplace.spec` (M0118-0009)
Type: **enabler — NOT a promotion** (the spec stays `defer`)

## Problem

`intra-grant-inplace.spec` exercises PostgreSQL's rule that a catalog ACL change
(`GRANT`/`REVOKE`) takes **no** heavyweight lock — its lock IS the catalog
tuple's `xmax` — while an *in-place* update of the same `pg_class` row
(`ALTER TABLE … ADD PRIMARY KEY`, which flips `relhasindex` true via
`heap_inplace_update`) must serialise behind that `xmax`.

Permutation 1 is the core case:

```
b1            -- s1: BEGIN
grant1        -- s1: GRANT SELECT ON intra_grant_inplace TO PUBLIC   (uncommitted)
read2         -- s2: SELECT relhasindex … → f
addk2(c1)     -- s2: ALTER TABLE … ADD PRIMARY KEY (c)   <waiting ...>  (blocks on grant1's pg_class xmax)
c1            -- s1: COMMIT  → addk2 completes
read2         -- s2: relhasindex → t
```

goopg served the `GRANT` as a `CompatNoopStmt` and `ADD PRIMARY KEY` ran
immediately, so `addk2` did not wait — first divergence at expected line L17.

## Fix

Replay the tuple-`xmax` wait directly, mirroring the just-landed database
sibling (design 0118-0098, `intra-grant-inplace-db`), since goopg serves
`pg_class` virtually (no real heap tuple / `xmax`):

1. **Parser** (`internal/parser/parser.go`): the GRANT/REVOKE scan, which already
   detected `ON DATABASE` (`CompatNoopStmt.DatabaseACL`), now also resolves a
   table target into the new `CompatNoopStmt.TableACL` (the relation name). The
   default GRANT object class is TABLE, so a bare `ON <ident>` and an explicit
   `ON TABLE <ident>` both yield the name; a schema-qualified `schema.table`
   yields the table component (`grantObjectName`); non-table object classes
   (`SCHEMA`/`SEQUENCE`/`FUNCTION`/…) are excluded (`grantNonTableClass`).

2. **Catalog** (`internal/catalog/catalog.go`): a mutex-guarded
   `tableACLChangeXID map[oid]xid` with `SetTableACLChangeXID(oid,xid)` /
   `TableACLChangeXID(oid)` — the per-relation analog of the `dbACLChangeXID`
   `atomic.Uint32`.

3. **Executor — record** (`execCompatNoop`, `operators_ddl.go`): a
   `TableACL`-bearing no-op looks up the relation, materializes this
   transaction's writer XID (`MaterializeWriterXID`) and stores it as the
   table's ACL-change xmax.

4. **Executor — wait** (`execAlterTableAddPrimaryKey`, `operators_ddl.go`): before
   creating the PK index, `waitForTableACLChange(tbl)` reads the table's recorded
   ACL-change xmax and `mvcc.WaitForXID`s on it. `WaitForXID` returns immediately
   when the XID is unset (`0`), is this transaction's own, or has already
   finished, so this is a no-op in the common case. The isolation runner decides
   `<waiting ...>` purely by a 300 ms timeout, so the XID-block reproduces the
   exact output.

## Result

First divergence advanced L17 → L62: permutation 1 now byte-identical
(`addk2 <waiting ...>` / `<... completed>` on `c1`). The remaining permutations
(3, 4, 7–11) require pg_class **rowmark** locking — `SELECT relhasindex … FOR NO
KEY UPDATE` / `FOR UPDATE` / `FOR KEY SHARE` and `DELETE FROM pg_class` taking a
real tuple lock on a virtual catalog row, plus `LockTuple` deadlock detection —
which is the genuinely Effort-L runtime shared-catalog MVCC-tuple-lock core. The
spec stays `defer`.

## Blast radius

Nil for normal usage. The wait fires only inside `ALTER TABLE … ADD PRIMARY KEY`
and only when a *concurrent uncommitted* GRANT/REVOKE touched the same table's
ACL; with no in-flight ACL change the marker is `0` and the wait returns
instantly. The parser change only annotates the already-existing GRANT/REVOKE
no-op path (no new statements accepted/rejected). No MVCC/storage/WAL surface.

## Gates

- `TestParseGrantTableACL` (new, parser) PASS; `internal/parser` + `internal/catalog` units PASS.
- Non-regression: `TestPort_IsolationIntraGrantInplaceDb` (shares the GRANT/DatabaseACL path) and `TestPort_IsolationTruncateConflict` (GRANT-on-table privilege) strict PASS.
- `go build ./...` + `go vet` (parser/catalog/executor) clean.
- pgbench smoke = pre-commit hook.
