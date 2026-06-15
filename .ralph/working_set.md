Task: M0110-0003 (pg_amcheck 003_check) — loop #19. PORTED the central
combined-corruption integration tier of 003_check.pl (its main check :347-365).
COMPLETE + ready to commit.

=== WHAT LANDED (this loop) ===
New `TestPort_PgAmcheck003CombinedCorruption`
(internal/testport/pgamcheck003_combined_test.go). The three sibling surrogates
(MissingIndexFork, MissingHeapFile, 004 page-overwrite) each inject ONE
corruption on a single-relation fixture. This tier injects all THREE classes —
removed btree index fork (tfork_idx), removed heap file (tfile), overwritten
heap line pointer (tpage, reusing corruptFirstLinePointerLength) — in a SINGLE
stop→corrupt→restart cycle (mirrors perform_all_corruptions :107-119), then
asserts ONE scoped pg_amcheck run reports all three upstream-verbatim regexes
together (exit 2, empty stderr). Proves pg_amcheck dispatch does NOT abort on the
first corrupt relation (removed-file case raises ERROR 58030). PURE faithful
port, ZERO engine change — goopg already does this.

Files:
- internal/testport/pgamcheck003_combined_test.go (new)
- docs/test-port/postgres-oracle-port-status.csv (AC-003 rationale) + regen .md
- docs/design/0110-0003-pg-amcheck-tap-port.md (new "Combined-corruption
  integration tier" section)
- .ralph/fix_plan.md (loop #19 PROGRESS), .ralph/deferral_ledger.md (loop #19)

Gates: gofmt + `go vet ./internal/testport` clean; build clean.
TestPort_PgAmcheck003CombinedCorruption PASS (11.1s); full pg_amcheck port suite
PASS (33.7s, no regression). TPC-H spotcheck N/A (test-only, zero engine change).

=== SURFACED (separate gap, out of scope) ===
goopg does NOT persist a `CREATE SCHEMA` pg_namespace row across a server
restart: a first `--schema s1` run was clean pre-corruption but reported `no
relations to check in schemas matching "s1"` after the required restart. Fixture
therefore uses `public` + one `--table` per relation (as every AC-003 surrogate
does). Catalog-durability capability, independent of amcheck.

=== NEXT STEP (resume) ===
Commit + push this loop. AC-003 remaining 003_check tiers all need missing
features: hash/gist/gin/brin/spgist index AMs, box/int4range/int4[] column types,
STORAGE EXTERNAL TOAST corruption, multi-DB orchestration; 005_opclass_damage
(CREATE OPERATOR CLASS + pg_amproc parity). Other open: M0095-0003 recvlogical
(030, logical decoding, large); M0110-0001 pg_dump 002 (catalog parity);
M0110-0002 pg_waldump 002 (index AMs + FPI); M0117-0006/7/8 (Effort-L, defer).
The CREATE SCHEMA cross-restart durability gap is a candidate small standalone task.
