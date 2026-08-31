# 0118-0048 — `DETACH PARTITION … CONCURRENTLY` parser-position fix (M0118-0008 enabler)

Status: accepted
Date: 2026-06-23
Milestone: M0118-0008 (DDL / VACUUM / maintenance concurrency)

## Summary

Fixes a parser bug that made the valid SQL
`ALTER TABLE parent DETACH PARTITION child CONCURRENTLY` (and the `FINALIZE`
variant) fail with a syntax error. This is **not a spec promotion** — it is a
bounded enabler that unblocks the very first step of all four
`detach-partition-concurrently-{1,2,3,4}` isolation specs (each currently dies
at `s2detach` with `syntax error … (got concurrently)`). After the fix the
statement parses and the executor performs a synchronous detach; the remaining
gap to a full promotion is the Effort-L two-phase concurrent-detach + snapshot
visibility semantics (see *Deferred*).

## The bug

`parseAlterTableAction` (`internal/parser/ddl.go`) handled
`DETACH PARTITION child [CONCURRENTLY|FINALIZE]` but consumed the optional
`CONCURRENTLY`/`FINALIZE` keywords **before** parsing the child name:

```go
if p.acceptIdentKeyword("detach") {
    _ = p.acceptKeyword(KwPartition)
    p.acceptIdentKeyword("concurrently")   // BUG: trailer comes AFTER the child
    p.acceptIdentKeyword("finalize")
    childName, err := p.parseObjectName()
    ...
}
```

For the real grammar (`DETACH PARTITION <child> [CONCURRENTLY|FINALIZE]`) the
current token at that point is the child name, so both `acceptIdentKeyword`
calls no-op, `parseObjectName` consumes `d_listp2`, and the trailing
`CONCURRENTLY` token is left unconsumed — producing
`syntax error at or near … (got concurrently)`. The previous spelling only
accepted the (non-standard) order `DETACH PARTITION CONCURRENTLY child`.

## The fix

Move the trailer acceptance to **after** `parseObjectName`, and record the
`CONCURRENTLY` flag on the AST node:

```go
if p.acceptIdentKeyword("detach") {
    _ = p.acceptKeyword(KwPartition)
    childName, err := p.parseObjectName()
    if err != nil { return AlterTableAction{}, err }
    concurrently := p.acceptIdentKeyword("concurrently")
    p.acceptIdentKeyword("finalize")
    return AlterTableAction{..., DetachPartitionChild: childName, DetachConcurrently: concurrently}, nil
}
```

New AST field `AlterTableAction.DetachConcurrently bool` (`ast.go`) records the
trailer so the deferred two-phase detach can branch on it. `FINALIZE` is
accepted and ignored (it completes a previously-cancelled concurrent detach —
not modelled yet). The executor's `AlterTableDetachPartition` case
(`operators_ddl.go`) is unchanged: it performs a synchronous detach
(`UnregisterPartitionChild` + clear `PartitionParentOID`/`PartitionBounds`) in
both the plain and `CONCURRENTLY` cases.

## Blast radius

Parser-only. `DETACH PARTITION child` (no trailer) is unaffected; the new path
only changes which token position the optional trailer is read from. No
executor, planner, or catalog behaviour changes (the synchronous detach is
identical to before). The new bool field defaults to `false` and has no live
consumer yet.

## Verification

- `TestParseAlterTableDetachPartition` (new, `internal/parser/alter_test.go`):
  plain / `CONCURRENTLY` / `FINALIZE` forms parse to `AlterTableDetachPartition`
  with the expected child name and `DetachConcurrently` flag.
- Full `internal/parser` package green.
- Probe of `detach-partition-concurrently-1.spec`: first divergence moved from
  a hard `syntax error … (got concurrently)` at `s2detach` (which aborted the
  whole permutation) to the expected-but-unmodelled `<waiting ...>` marker — the
  detach now executes and the post-detach `SELECT * FROM d_listp` returns the
  correct rows; only the concurrent-wait timing differs.

## Deferred (full promotion blockers — ledger 2026-06-23)

`detach-partition-concurrently-{1,2,3,4}` (and the sibling
`partition-concurrent-attach`, `alter-table-4`) need **transactional-DDL
cross-session catalog visibility**: with goopg's single shared in-memory
catalog a DETACH/ATTACH is visible to other backends immediately, whereas PG
makes the partition disappear from concurrent READ COMMITTED transactions only
after the detacher waits out older snapshots, and keeps it visible to
REPEATABLE READ transactions until they commit. `DETACH … CONCURRENTLY` further
requires the two-phase wait (the detacher hangs until every transaction that
could still see the partition terminates) and `relpartbound` flipping to NULL at
commit. These are Effort-L and tracked as the M0118-0008 hard tail.
