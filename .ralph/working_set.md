# Working Set (carried from loop 3, 2026-06-13)

## Completed this loop — M0100-0006b FINAL (and M0100-0006) → both CLOSED

**UPSERT DO UPDATE: HOT-equivalent index-key reuse.** Perm 5 of
`insert-conflict-specconflict` now fully passes — the test SKIP is gone.
`TestPort_IsolationInsertConflictSpecconflict` PASS (all 5 perms).

Root cause: PG's `ON CONFLICT DO UPDATE` (no indexed column changed) is a HOT
update → inserts ZERO index tuples, never re-evaluates the non-unique expression
index. goopg (no HOT) re-inserted every index entry on `applyUpdate`,
re-evaluating `blurt_and_lock_4(key)` → 2 extra NOTICEs after `s2_commit`.

Fix (all in `internal/executor/operators_upsert.go`):
- `upsertOp.specIndexKeys map[uint32][]byte` + `specInsertedLeaf Row` — reset per
  source row (top of the Next() loop), populated by `applyInsert`.
- `maintainNonArbiterIndexesCapture` (new) — applyInsert path: inserts + captures
  each non-arbiter index key (the one legit `blurt_and_lock_4` eval).
- `maintainNonArbiterIndexesForUpdate` (new) — applyUpdate path: reuses cached key
  when `indexKeyUnchangedFromSpec` proves referenced base cols unchanged; else
  evaluates (prior behaviour).
- `indexKeyUnchangedFromSpec` + `collectExprColumnNames` (conservative AST walker,
  unknown node → re-evaluate) — new helpers.
- Removed orphaned `maintainUniqueIndexesForInsertSkipArbiter`
  (`operators_storage.go`).
- Design doc: `docs/design/0100-0006b-upsert-hot-index-key-reuse.md` + README index.

## Gates run this loop
- go build ./... : PASS
- go test ./internal/executor/ : PASS
- TestPort_IsolationInsertConflictSpecconflict : PASS (5/5 perms)
- TestPort_IsolationInsertConflict* + TestPort_IsolationMerge* : PASS (no row-count regression)
- scripts/tpch-spotcheck.sh : SKIPPED (no data dir; change touches only ON CONFLICT index maint)
- make ralph-state-guard : (run immediately before status block)

## Next task (next loop)
21/21 RC isolation tests now pass. Next: **M0100-0005** E2E confirmation —
run `go test -v -run TestPort_Isolation -timeout 30m ./internal/testport/`,
confirm all 21 report pass, then flip the 21 specs in
`docs/test-port/executable-isolation-tests.md` to `status=port`/`pass_required=yes`,
mark M0096-0005 + M0096-0013 `[x]`, set milestone 0100 + README to `accepted`.
(Note: M0100-0005 lists "Depends: Close of M0107" — verify M0107 status first.)
Alternatively **M0100-0007** (MergeUpdate: `old`/`new` aliases + `merge_action()`).
(idle — nothing else in flight)
