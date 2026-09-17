Task: P0-E4 — reproduce the catalog-xmax/commit-status loss defect.
LANDED AND COMMITTED this loop (commit cd330108c).

Files: internal/testport/p0e4_catalog_xmax_loss_test.go (new — 3 regression
tests), docs/design/0100-0149/p0-e4-catalog-xmax-loss-repro.md (new design
note), docs/design/README.md (p0-e4 row appended), .ralph/fix_plan.md
(P0-E4 flipped to `[x]` with a Done note).

Key symbols: `stampCatalogRowsTuple` (internal/executor/operators_ddl.go:18121)
stamps a catalog row's OLD version with xmax + the XmaxCommitted hint BEFORE
commit; `deleteCatalogRowsForOID` (:18186) is its caller, used by every
in-place catalog-row-replacing DDL path (e.g. `finishPrimaryKeyConstraint`,
:12252, adopting a UNIQUE INDEX via `ADD CONSTRAINT ... PRIMARY KEY USING
INDEX`); `ProcessRollbackUndos` (internal/executor/operators_tx.go:388) never
undoes that stamp (disk analogue of M0143-0008); `scanCatalogHeapRows`/
`catalogRowLive` (internal/initdb/catalog_heap_reload.go:75,43) discard ANY
row with Xmax != InvalidTransactionID unconditionally at startup, never
checking CLOG for whether that xmax transaction actually committed.

Findings: reproduced LIVE (not just by code reading) on a throwaway
`cluster.New`-spawned goopg server (real `psql` client via `StartPSQL`/`PSQL`,
inside `CREATE DATABASE r; \c r` per the false-negative warning about the
default DB's JSON catalog cache). All THREE abort paths lose table `t`
identically after restart — explicit ROLLBACK, client killed without
ROLLBACK, and `goopg stop -mode immediate` mid-transaction (the incident's
own shape) — `to_regclass('t')` NULL, `SELECT count(*) FROM t` → 42P01. The
heap DATA FILE (found via relfilenode) always survives in all three cases —
this is a catalog-only loss, confirming the loader's blanket xmax filter is
the single common root cause, independent of which abort mechanism triggered
it. Full repro table + code citations in the design note.

Next step: select **P0-E5** next (fix the defect + tick M0143-0008 in the
same commit, per the banner's P0 chain: E4 -> E5 -> E6[owner] -> E7). Two
pieces required together (fixing only one leaves live vs. reloaded catalog
disagreeing): (1) disk side — `scanCatalogHeapRows`
(internal/initdb/catalog_heap_reload.go:75) must resolve a non-zero Xmax's
commit status via CLOG (subtransactions resolved to parent) instead of
treating any non-zero Xmax as dead outright; the `XmaxCommitted` hint
(`stampCatalogRowsTuple`) must only be set once commit is confirmed (verify
the DU-002 runtime-visibility comment at operators_ddl.go:18152-18161 doesn't
regress); (2) in-memory side — M0143-0008 (ALTER TABLE not undone on
ROLLBACK). Once fixed, remove the three `t.Skip` calls in
internal/testport/p0e4_catalog_xmax_loss_test.go — they are P0-E5's
regression lock. Gates for P0-E5 per fix_plan: units + sf025 sweep + the new
tests must PASS; `tpch-spotcheck` will be SKIP-BLOCKED by the `:65433` hold
(the one allowed exception to G6 — commit with a `ledger:` line naming P0-E7
as the re-run owner). If the banner has moved on by the time this is read,
re-check `## Current Priority` fresh per the Precedence rule — the P0 chain
(items 0/E4-E7) still gates everything below it either way.

Gates run: `go build ./...` clean. `go vet ./internal/testport/` clean.
`go test -run TestPort_P0E4 ./internal/testport/`: all 3 SKIP (as designed)
— confirmed by temporarily removing the `t.Skip` calls locally (not
committed) and re-running each test individually, all 3 FAILed exactly as
the design note's table predicts, then restored the skips before commit.
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`: all packages
PASS except the pre-existing, already-documented `internal/parser`
GroupedJoinUnaliased AST-drift (60+ test functions, unrelated — this loop
touched no parser file). `make ralph-state-guard`: same status="running"/
progress="completed" drift as prior loops (previous loop's clean-exit
marker), auto-repaired, then passed (re-checked clean after commit).
Pre-commit hook's pgbench smoke: PASS (ran automatically on this loop's
commit; commit-msg hook rejected the first attempt's `p0(e4):` area label
as "claims a code change" — test-only changes need `test(...)`, not `p0(...)`
/`m0143(...)` etc. — re-committed with `test(p0-e4): ...` and it passed).

In-flight: none. No servers or background processes left running this loop
(the test cluster's own t.Cleanup stops it; the temp file used to
temporarily disable the skips during local verification, /tmp/p0e4_backup.go,
was removed before this loop ended).
