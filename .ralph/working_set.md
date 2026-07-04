(idle — nothing in flight)

Last completed (this loop, 2026-07-04): landed root-0021's "VALID UNTIL heap
round-trip" fix, which a prior loop had already fully implemented (code +
design doc + deferral-ledger row + extended test) but left uncommitted when
it was cut off. This loop verified it (build/vet/targeted tests/tpch-spotcheck
all green) and committed+pushed as `de8c46c8`.

Also resolved (informational, no code impact): SessionStart's concurrency
guard flagged a possible second ralph_loop.sh writer. Investigation found (1)
part of the flag was a false positive — guard.py miscounted my own
ralph_loop.sh wrapper ancestors as "independent"; (2) a genuinely separate
peer loop ("Tree B"/"Loop #2"/"Loop #3", distinct PIDs on pts/15) DID run
concurrently for a few minutes, correctly detected the conflict itself, made
NO working-tree edits, and self-terminated before I finished my own gates —
by the time I re-checked (`pgrep -af ralph_loop.sh` + `.git/index.lock`),
only my own tree remained. Proceeded as sole writer. Details in memory
[[concurrent_ralph_loops_corrupt_tree]] (2026-07-04 entry).

Gates run this loop: `go build ./...` clean; `go vet ./internal/executor/...
./internal/initdb/...` clean; `go test ./internal/initdb/... -run
TestPgAuthidSyncLoadRoundTrip` PASS; `go test ./internal/executor/...
./internal/initdb/... ./internal/server/... ./internal/catalog/...` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); pgbench smoke via the
pre-commit hook PASS (commit `de8c46c8`, pushed to
`align-data-structure-with-pg`).

Next step: per `.ralph/fix_plan.md`'s Current Priority banner, pick one of:
(a) the `updateViaIndex` inheritance/partition fan-out discovery (start with
a plain non-view two-table INHERITS regression test `UPDATE parent SET
val=1 WHERE id=X` where id=X exists only in a child, before touching
`internal/executor/operators_storage.go`'s `updateViaIndex`); or (b)
continue the M0119-0004 pg_dump catalog-view parity battery / next
unresolved DU-002 slice from `.ralph/deferral_ledger.md`.
