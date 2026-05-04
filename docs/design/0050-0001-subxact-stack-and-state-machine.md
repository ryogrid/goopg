# 0050-0001 — Subtransaction stack and state machine

**Status:** draft
**Date:** 2026-05-04
**Milestone:** 0050 — Savepoints and subtransactions
**Supersedes:** —

## Context

`TransactionState` today is a single struct per session: one xid, one
snapshot, one rolled-back-or-committed result. Savepoints introduce
nesting: `BEGIN; ...; SAVEPOINT s1; ...; SAVEPOINT s2; ROLLBACK TO s1;
COMMIT;`. The session must keep a stack of subxact states and update
the right subset on each verb.

## Plan

1. New struct in `internal/mvcc/`:
   ```go
   type SubTransactionState struct {
       Id        SubTxnId       // monotonic counter, scoped to top-level xact
       Name      string         // SAVEPOINT name, "" for implicit
       Parent    *SubTransactionState
       SubXid    Xid            // assigned lazily when first row written
       Snapshot  *Snapshot      // copy of current snapshot at push
       Status    SubXactStatus  // active | committed | aborted
   }
   ```
2. `TransactionState` grows a `Stack []*SubTransactionState`. Index 0
   is the top-level xact (existing fields move into it).
3. Verbs:
   - `SAVEPOINT name` → push new entry, copy current snapshot,
     name-set; record in WAL once a subxact xid is allocated.
   - `RELEASE SAVEPOINT name` → pop the named entry and any above it,
     promoting their (a) inserted rows' xmin (still subxact xid; the
     parent's commit will commit them too), (b) acquired locks
     (transferred to parent's lock owner), (c) WAL-recorded actions
     (no-op — they all rode on subxact xids that the parent will
     commit).
   - `ROLLBACK TO SAVEPOINT name` → pop the named entry and any above
     it; mark each as aborted; release locks acquired *only* by them;
     emit subxact-abort WAL records; **then push a fresh entry with
     the same name** (matches upstream).
   - `ROLLBACK` (top-level) → mark every entry on the stack aborted in
     reverse order, then mark top-level aborted.
4. Subxact xid allocation is **lazy** — only when a subxact actually
   writes (insert/update/delete or DDL) does it consume a fresh xid via
   `XidAdvance`. Mirrors upstream's `AssignTransactionId` chain.

## Definition of Done

- Stack push/pop/rewind covered by unit tests in `internal/mvcc/`.
- Lock-owner re-assignment correctness — locks acquired by a subxact
  promoted to parent on RELEASE, dropped on ROLLBACK TO.
- Visibility tests (defer to 0050-0002).

## Upstream reference

- `postgres/src/backend/access/transam/xact.c` —
  `BeginInternalSubTransaction`, `RollbackToSavepoint`,
  `ReleaseSavepoint`, `EndSubTransaction`, `AbortSubTransaction`.
- `postgres/src/include/access/xact.h` — `TransactionState` shape.

## goopg references

- `internal/mvcc/transaction.go` — current single-state type.
- `internal/lock/` — lock owners need subxact awareness.
- 0050-0002 — visibility model.
- 0050-0003 — WAL records.
