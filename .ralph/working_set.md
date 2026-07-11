(idle — nothing in flight)

## Loop summary (2026-07-12, loop #73)

**Nightly triage:** action-items batch `20260711-011536` — all 3 AI items
(IsolationTimeouts / TuplelockUpgradeNoDeadlock / PgWaldumpVacuumPruneRoundtrip)
already `[x]` in M-NIGHTLY (co-load timing flakes). No new nightly work.

**Task — code-audit resolution of unimplemented_feat #178 "pg_subtrans
truncation is deferred" (task_id M0117-0003). COMPLETE + committed.**

Finding: the item was STALE. Its `code_audit` field literally read "unclear...
not verified". Verified pg_subtrans truncation is fully implemented and wired
end-to-end by M0122-0009 (commit 41e119b8):
- `internal/mvcc/subxact_slru.go` `SubtransSLRU.TruncateBefore` — unlinks
  segment files whose highest page wraparound-precedes the cutoff.
- `internal/mvcc/subxact_visibility.go` `SubxactMap.Truncate` — drops in-memory
  parent/abort entries older than horizon + cascades to SLRU.
- `internal/initdb/open.go:1628` `TruncateSubtransFn` — same conservative
  `min(datfrozenxid, OldestXmin)` horizon as `TruncateCLOGFn`.
- `internal/wal/checkpointer.go:665` invokes it post-checkpoint (durable order).
- Tests: `mvcc/subxact_truncate_test.go`, `wal/checkpointer_test.go`
  `TestCheckpointerCallsTruncateSubtransFn` + `...ErrorIsNonFatal`.

Landed: unimplemented_feat.json #178 status open→resolved, confidence→high,
code_audit rewritten with the full wiring citation + PG parity note (goopg's
horizon is at-least-as-conservative as PG's GetOldestTransactionIdConsideredRunning).
Open count 78→77.

No deferral (nothing left unimplemented). No design-doc change (metadata-only,
no subsystem code touched).

Gates: mvcc + wal subtrans/checkpointer tests PASS; JSON re-validated;
ralph-state-guard consistent (auto-repaired stale completed marker); pgbench
smoke via pre-commit hook.

In-flight: none
