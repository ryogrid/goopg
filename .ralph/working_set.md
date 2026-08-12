(idle — nothing in flight)

M0131-S21h CLOSED and filed. **The opcode arm was green; the record was not
being applied.**

Landed:
- `internal/storage/heap.go` — `PageApplyHeapDeleteRedo`,
  `PageApplyHeapUpdateOldRedo`, `pageSetPrunablePG`, shared
  `heapRedoTupleOffset` (siblings of S21a-2's `PageApplyHeapLockRedo`).
- `internal/wal/recovery.go` — both heap arms now translate
  `infobits_set` / `old_infobits_set` via `xlogHeapLockInfomaskBits` and pass
  the record xid + the two delete flags.
- `internal/wal/pg_assembled_emit.go` — `xlhDeleteIsSuper` (0x08),
  `xlhDeleteIsPartitionMove` (0x10).
- `internal/wal/heap_delete_update_infobits_pg_test.go` (new, 6 tests).
- `docs/design/0131-0015-pg-wal-opcode-coverage.md` §S21h + F36/F37/F38.

Worth carrying:
- **A green dispatch arm proves the opcode is routed, NOT that the record is
  applied.** S21's whole line asked the first question; asking the second of
  two arms landed in B0.2 found `infobits_set` discarded — the ONLY place
  `xl_heap_delete`/`xl_heap_update` says its xmax is a MultiXactId. Generalise:
  re-audit an arm against the upstream routine's body, not its `case`.
- The defect was invisible because goopg's own emit hardcodes
  `infobits_set = 0`, so goopg↔goopg replay tests could never see it — the
  same emit-side blindness pattern S21's own closure noted.
- Enumerating `heap_xlog_delete` line by line (rather than fixing only the bit
  the multi finding named) found 4 further omissions incl. `IS_SUPER`, which
  kills a speculative tuple by clearing **xmin**, not by stamping xmax.
- Deliberately NOT reused: the producer `PageStamp*` helpers. Runtime stamps
  what goopg is doing now (always a plain xid); redo must reproduce whatever PG
  decided. Their newest-wins `pd_prune_xid` rule is upstream-inverted — ledgered.

Gates: `internal/wal` PASS, `internal/storage` PASS, `internal/mvcc` +
`internal/executor` PASS, `^TestE2E_` family PASS (109 s), UNITS PASS,
`go build ./...` + `go vet` clean, pgbench smoke via the commit hook. 2 break
directions proven fail-when-broken by scripted revert.

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501`; all four
`## AI-` items already filed under M-NIGHTLY — nothing new to file.

Next loop (banner = M-NIGHTLY filing, then M0131): the only unchecked M0131
items are **S9** (nothing left to run — S9.4 measured and deferred with the
S9.4a..d successor decomposition in design `0131-0009`; a loop could formally
close it and file the successor milestone) and **S24** (MultiXact durable SLRU;
explicitly deferred, re-arm trigger is the `t.Skip` at
`internal/testport/e2e_goopg_crashstart_on_pgdata_test.go:221`, but it still
lacks its own ledger row — filing that is a legitimate short loop).

In-flight: none.
