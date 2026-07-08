Task: M0122-0006 follow-up 3 — real opclass/collation name→OID registry
for `pg_index.indclass`/`indcollation` (picked from working_set's "good
candidates" list + design doc's "still open" item). COMPLETE and about to
be committed this loop (not left mid-flight).

Files: internal/catalog/catalog.go (pg_index `VirtualRows`: classOIDs/
indcollation loops now call the new shared resolvers instead of a
hardcoded, PG-inaccurate per-type switch that ignored `ColOpClasses`
entirely; new `builtinColumnOpclassOIDs` map, `defaultColumnOpclassNameForType`,
`resolveColumnOpclassOID` (unexported helper), and exported
`ResolveIndexColumnOpclassOID`/`ResolveIndexColumnCollationOID` methods on
`*InMemory`, added to the `catalog.Catalog` interface); internal/catalog/
catalog_test.go (new `TestPgIndexIndclassIndcollation`); internal/executor/
pg18_user_catalog_rows.go (`buildUserPGIndexRow` signature gained a
`cat catalog.Catalog` first param, now writes real indclass/indcollation
OIDs via the shared resolvers instead of always-zero); internal/executor/
operators_ddl.go (2 call sites now pass `ctx.Catalog`); internal/executor/
pg18_user_catalog_rows_test.go (2 call sites pass `catalog.NewInMemory()`);
docs/design/0122-0006-index-column-order-restart-persistence.md (new
"Follow-up 3 (2026-07-08)" section); docs/design/README.md (0122-0006 row's
"Still open" sentence replaced with the follow-up-3 summary);
.ralph/deferral_ledger.md (flipped the 2026-07-08 "follow-up 2 of 2" row's
status to `resolved`, appended a new `-` row for the remaining decode-side
gap — see Findings).

Key symbols: `ResolveIndexColumnOpclassOID`/`ResolveIndexColumnCollationOID`/
`builtinColumnOpclassOIDs`/`defaultColumnOpclassNameForType` (internal/
catalog/catalog.go, ~line 18170+, right after `builtinOpclassOIDByName`);
`buildUserPGIndexRow` (internal/executor/pg18_user_catalog_rows.go).

Findings: fixed the LIVE (non-restart) pg_index rendering AND the
heap-row WRITE side (`buildUserPGIndexRow`, used by both restart recovery
and a real PG18-standby's direct heap scan) in one loop, since they're
sibling paths that must not diverge (hard-won rule #2). Both now share the
exact same two resolver methods via the `catalog.Catalog` interface — no
duplicated logic. All builtin OID values were verified against a REAL
running PostgreSQL 18.3 instance (there's a leftover `postgres -p 5599`
process from an earlier session at /tmp/pgtsconfig_test — queried its
`pg_opclass` directly rather than trusting `pg_opclass.dat`, since most
entries there are genbki-autonumbered, not BKI-frozen). The OLD hardcoded
per-type switch was flat-out wrong vs real PG (e.g. int2_ops rendered as
1970 instead of 1979, text_ops as 1994 instead of 3126) — no test asserted
those wrong values, so correcting them was safe.
REMAINING GAP (recorded in ledger, NOT silently dropped): the heap-row
READ/decode side — `catalog.PGIndexRow`/`DecodePGIndexPhysicalRow`
(internal/catalog/codec.go) still don't decode `indclass`/`indcollation`,
so `internal/initdb/open.go`'s `loadUserIndexesFromHeap` can't restore
`idx.ColOpClasses`/`ColCollations` (name strings) after a *checkpointed*
restart. An uncheckpointed crash restart is unaffected (WAL's
`CreateIndexPayload` extension block already carries the name strings
directly, no OID round-trip needed). See the ledger's new "follow-up 3"
row for the exact resume point (decode oidvector → `PGIndexRow.IndClass`/
`IndCollation`, build the OID→name reverse lookup, wire into
`loadUserIndexesFromHeap` Pass 3).

Next step: pick a fresh item. Good candidates: (1) the just-recorded
decode-side follow-up above (now well-scoped, forward registry already
exists — just needs OID→name inversion + wiring); (2) M0007 "eager
next-segment lookahead for WAL preallocation" (background-goroutine scope,
read `unimplemented_feat.json` code_audit ~line 496 first); (3) real
password/MD5/SCRAM auth on the replication path (blocked on server-side
walsender auth-checking landing first, per M0005's ledger row).

Gates run: go build ./... clean. go vet ./... clean (repo-wide). go test
./internal/catalog/... ./internal/executor/... ./internal/initdb/...
./internal/planner/... ./internal/wal/... PASS (one transient flake in
`TestStripeAppendConcurrentDrainConsistency` under concurrent host load
from the nightly-batch peer process — reran in isolation 3x clean, then
reran the whole wal package clean; unrelated to this change, timing-only
concurrency test). scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33).
RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh PASS (0
failed, all 3 workloads, run twice). Live-verified against the real
`cmd/goopg` binary via psql (see Findings). make ralph-state-guard: same
recurring benign status/progress reconciliation as every prior loop,
auto-repaired.

In-flight: none. A concurrent nightly-batch peer process (ci/batch/
run-nightly.sh + stage-testport.sh) was running throughout this loop in
the same tree, same as prior loops — no file conflicts observed (disjoint
files), consistent with the established "concurrent Ralph loops" pattern.
Nightly-triage: `ci/logs/action-items.md` mtime still 2026-07-07 03:52,
unchanged from last loop's check — no new triage needed next loop unless
that file's mtime has moved.
