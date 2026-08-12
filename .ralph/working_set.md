(idle — nothing in flight)

M0131-S21f LANDED (loop #171) — the last three PG-format redo paths that refused
where upstream skips now share one RBM_NORMAL prologue.

Files: `internal/wal/recovery.go` (`replayDecodedXLogHeapDelete`,
`replayDecodedXLogHeapPrune`, `replayExistingXLogBlock` all route through
`redoExistingHeapPageForBlock`; its comment now documents the widened
deviation), new `internal/wal/redo_blknotfound_pg_test.go`, design
`docs/design/0131-0015-pg-wal-opcode-coverage.md` §"S21f" + Guard 11 (old 12
renumbered to 13), fix_plan S21f checked, 1 ledger row.

Worth carrying: the four-line "NBlocks + ReadBlock + hard error" prologue had
been copied FIVE times and had already drifted once — the consolidation matters
more than the behaviour flip. And the flip is not free: the invalid-page-table
deviation the helper documents was written for lock/confirm ("a lost stamp is
bookkeeping about a transaction the crash ended anyway") and that argument does
NOT cover a lost prune or btree deletion on a page that does survive. Ledger row
names the resume point (an invalid-page map with forget-paths, checked at the
end of `Recover`).

Gates: `internal/wal` + `internal/storage` + `internal/access/btree` PASS,
`-race` on the touched wal tests PASS, UNITS precommit PASS (warm cache), S28
reverse crash E2E PASS, both cold-start E2Es (`GoopgColdStartOnPGDataDir`,
`PGColdStartOnGoopgDataDir`) PASS, pgbench smoke via the commit hook,
`make ralph-state-guard` OK (auto-repaired the stale completed marker).
Fail-when-broken proven: restoring the hard error inside the shared helper fails
all three sub-tests.

Note for the next loop: the testport E2Es launch the server via
`go run ./cmd/goopg`, so `go test`'s cache is BLIND to `internal/wal` changes —
a cached PASS there is stale. Force those with `-count=1` (one-off probe, not a
gate run).

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501`; all 4
`## AI-` items already filed under M-NIGHTLY, nothing new.

Next loop (banner = M-NIGHTLY filing, then M0131): S23 (the cheap
LogicalMessage / ReplicationOrigin / Generic / CommitTs tail) is the obvious
next slice; the two S21g ledger rows (new_xmax, ALL_VISIBLE_CLEARED) are small
enough to fold into it.

In-flight: none.
