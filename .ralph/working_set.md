Task: M0122-0007 follow-up 8 — slice 4 sub-slice 4b-i ("namespace the
table/index maps, internal only"), per docs/design/0122-0018-per-database-
catalog-namespace.md. COMPLETE and ready to commit this loop.

Files: internal/catalog/catalog.go (new `tableNamespace` struct +
`namespaces map[uint32]*tableNamespace` field replacing `tables`/
`indexes`/`byTable`, new `(c *InMemory) ns(dbOid uint32) *tableNamespace`
accessor, `NewInMemory` pre-seeds `namespaces[DefaultDBOid]`, all 226
`c.tables`/`c.indexes`/`c.byTable` call sites -> `c.ns(DefaultDBOid).*`),
9 internal/catalog/*_test.go files (27 more direct-field refs, same
substitution), docs/design/0122-0018-per-database-catalog-namespace.md
(4b split into landed 4b-i / planned 4b-ii), docs/design/README.md
(index row updated), .ralph/fix_plan.md + .ralph/deferral_ledger.md
updated (new deferral row: 4b-ii, the public-signature threading, still
not started).

Key symbols: `catalog.tableNamespace`, `catalog.(*InMemory).ns(dbOid)`
(internal/catalog/catalog.go, right before NewInMemory) — the ONE new
accessor 4b-ii will build on. Locking contract: ns() does NOT lock c.mu
itself (not reentrant); relies on namespaces[DefaultDBOid] being
pre-seeded once inside NewInMemory (single-threaded) so its lazy-create
branch stays dead code until 4d starts seeding real per-database
namespaces under a held write lock. Every one of the 226+27 replaced
sites already used receiver `c` (verified via grep before editing) —
confirmed purely mechanical, no other-receiver edge case existed.

Gates run (all PASS): go build ./... clean; go vet ./... clean; go test
-short $(go list ./... | grep -v /internal/testport) (full repo, short
mode); go test ./internal/catalog/... ./internal/executor/...
./internal/server/... (targeted, non-short); scripts/tpch-spotcheck.sh
(Q12=2/Q13=33); RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh
(0 failed, all 3 pgbench workloads); make ralph-state-guard OK
(self-repaired stale prior-loop marker, same pattern as last loop).

In-flight: none. Note: `analysis/tpch-explain-baseline.md` picked up an
unrelated auto-regenerated diff (Q17/supplier row) from running the full
`go test` suite (internal/testutil/tpch/index_utilisation_test.go writes
this file as a side effect) — deliberately left OUT of this loop's commit
since it's unrelated to 4b-i; still sitting modified in the tree, safe to
either commit standalone or ignore.

Next step for a future loop: **4b-ii is the next resume point** (see the
design doc's "4b-ii — Give catalog entry points an explicit dbOid
parameter" section, and fix_plan.md's `M0122-0007` follow-up 8 entry).
Thread an explicit `dbOid uint32` parameter through every public catalog
entry point (`LookupTable`, `CreateTable`, `DropTable`, `LookupIndex`,
`CreateIndex`, `DropIndex`, `RenameTable`, `RenameIndex`, `AllTables`,
`AllIndexes`, `TablesInSchema`, `RegisterRealTable`,
`TryRegisterUserTable`, OID-keyed lookups, ...) now backed by `c.ns(dbOid)`
from 4b-i, and update every external caller (hundreds of sites in
internal/executor and internal/planner) to pass `catalog.DefaultDBOid`.
This crosses package boundaries (unlike 4b-i's single-file mechanical
pass) — expect to touch dozens of files; budget it as its own
self-contained pass (or worktree-isolated pass) and do NOT also start 4c
in the same loop. Re-run the full catalog/executor/server + short-mode
whole-repo suites plus tpch-spotcheck/pgbench-smoke gates after.
