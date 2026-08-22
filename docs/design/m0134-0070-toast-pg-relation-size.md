# M0134-0070: `pg_relation_size(reltoastrelid)` on a synthetic TOAST relation

Status: accepted. Scope: `strings.sql` regress case — the toasttest bucket
(`SELECT pg_relation_size(reltoastrelid) = 0 AS is_empty FROM pg_class WHERE
relname = 'toasttest';`, strings.sql:593-604; PG expects `f` after toasting and
`t` after TRUNCATE, goopg prints a blank/NULL value row in both).

## Problem

`reltoastrelid` is populated in goopg's pg_class virtual builder
(`internal/catalog/catalog.go:7269-7273` — `t.OID + toastRelidOffset` when
`tableHasToastRelation(t)`), so the column resolves to a real OID. But
`evalPgRelationSize` (`internal/executor/expr.go:5107`) then calls
`relationFileNodeForOID` (`expr.go:5094`), which only knows how to resolve
`LookupTableByOID` and `LookupIndexByOID`. A synthetic TOAST OID (parent OID +
`toastRelidOffset` = 100_000_000, a virtual pg_class row with no table/index
registry entry) matches neither, so it returns `(RelFileNode{}, false)` and
`evalPgRelationSize` returns `NullDatum` (`expr.go:5119-5122`). `NULL = 0`
evaluates to NULL, so the value row serializes blank.

The toast heap **does** exist on disk: `ToastLargeColumnsIfNeeded`
(`internal/executor/toast.go:199`) writes chunks to the fork addressed by
`ToastRelFor(mainRel)` = `{DBOid, RelOid+100M, MainFork}` — the same RelFileNode
`cat.ToastRelFileNode` returns. Once the OID resolves, `relationForkSize`
(`expr.go:5070`) reports the real non-zero size.

PG oracle: `postgres/src/backend/utils/adt/dbsize.c` — `pg_relation_size` (line
364) → `try_relation_open` (NULL only for a dropped/nonexistent relation, lines
371-381) → `calculate_relation_size` (line 326), which returns 0 when the fork
file does not exist (stat ENOENT → break). A `reltoastrelid` is a real
`relkind='t'` relation, so PG opens it and returns the toast heap's main-fork
size. Semantic to preserve: NULL is correct only for a *nonexistent* relation; a
live toast OID must resolve to a number.

## Design

Add an exported helper on `*catalog.InMemory` that maps a synthetic TOAST
**relation** OID to its main-fork RelFileNode, and call it from
`relationFileNodeForOID`:

```go
// ToastRelFileNodeByOID resolves a synthetic TOAST relation OID (parent OID +
// toastRelidOffset, the range [100M, 200M)) to its main-fork RelFileNode.
// Returns false for the TOAST index range [200M, 300M) and for non-TOAST OIDs.
func (c *InMemory) ToastRelFileNodeByOID(toastRelOID uint32) (storage.RelFileNode, bool) {
	if toastRelOID < toastRelidOffset || toastRelOID >= toastIndexOidOffset {
		return storage.RelFileNode{}, false
	}
	parent, ok := c.ToastParentTable(toastRelOID)
	if !ok {
		return storage.RelFileNode{}, false
	}
	return c.ToastRelFileNode(c.RelFileNode(parent))
}
```

Then `relationFileNodeForOID` (`expr.go:5094`) gains a third branch between the
index lookup and the final `return false`:

```go
	if toastRel, ok := cat.ToastRelFileNodeByOID(oid); ok {
		return toastRel, true
	}
```

The helper lives in `catalog.go` because that is where the unexported offsets
(`toastRelidOffset`/`toastIndexOidOffset`, `catalog.go:1011/1020`) and the
existing `ToastParentTable` (`:1232`)/`ToastRelFileNode` (`:1261`)/`RelFileNode`
(`:21109`) already are; `storage` is already imported there. It deliberately
**restricts to the relation range** — a toast *index* OID (`[200M, 300M)`) must
not be routed through `ToastRelFileNode`, which returns the toast *relation*'s
relid (the wrong file). The index range keeps returning false → NULL, which is
the current (and an acceptable, out-of-scope) answer since goopg materializes no
physical toast index file.

Both `relationFileNodeForOID` callers benefit: `evalPgRelationSize`
(`expr.go:5119`) and `evalPgTableSize` (`expr.go:5157`) now take a toast relid
directly, a strict improvement over the current NULL
(`pg_relation_size('pg_toast.pg_toast_N'::regclass)` works). For
`evalPgTableSize` with a toast relid, the separate `cat.ToastRelFileNode(rel)`
step (`expr.go:5162`) already returns false for a toast-oid input (`tableByOID`
finds no table), so there is no double count.

## Out of scope / deferred

- **J2 — `toast_tuple_target`**: the toast decision uses a fixed
  `ToastThreshold = 2000` (`toast.go:19`) and ignores the table's
  `toast_tuple_target` (`catalog.Table.ToastTupleTarget`, stored at
  `operators_ddl.go:3514`; 0 = default). The fixture's second case
  (`ALTER TABLE ... SET (toast_tuple_target = 4080)` → 3000-byte values stay
  inline → empty toast heap → `t`) still diverges after J1. Threading the
  effective target into `ToastLargeColumnsIfNeeded` touches the shared
  runtime-toast path (every toasting INSERT), wider blast radius than this
  bucket; recorded in the deferral ledger. J1 alone closes the first hunk.
- **TOAST index OIDs** — `pg_relation_size(pg_toast.pg_toast_N_index)` keeps
  returning NULL; faithful sizing would need a physical toast index fork, which
  goopg does not materialize.

## PG oracle citations

`postgres/src/backend/utils/adt/dbsize.c:326` (`calculate_relation_size`, 0 on
ENOENT), `:364` (`pg_relation_size`), `:371-381` (`try_relation_open` NULL-only-
on-nonexistent); toast relid as a real `relkind='t'` relation per
`postgres/src/include/catalog/pg_class.h`.
