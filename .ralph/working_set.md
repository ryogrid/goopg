Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 55 COMPLETE
(committing this loop). NEXT loop starts on slice 56.
NOTHING in flight after commit.

=== DONE (loop #10) — DU-002 slice 55 (COMMENT ON COLUMN round-trip) ===
COMMENT ON TABLE already round-tripped, but COMMENT ON COLUMN was silently
DROPPED. pg_dump emits the canonical 3-part `COMMENT ON COLUMN
schema.table.col`; goopg's parser handled only the bare 2-part `table.col`
(parseObjectName consumes 2 dotted parts; the column case never read the
trailing `.col`), so the 3-part form raised "expected IS after object name" —
an error the server's COMMENT fallback SILENTLY SWALLOWS (so nothing reached
pg_description, comment vanished).
FIX (parser-only): internal/parser/parser.go parseCommentOnTail column case now
checks for a trailing `.col` after parseObjectName — present → parsed name is
the (optionally schema-qualified) table + trailing ident is the column;
absent → 2-part table.col mapping stands. execCommentOn already resolved a
schema-qualified ObjName via LookupTable, so no executor change.
Files: internal/parser/parser.go (column case),
internal/parser/comment_on_test.go (NEW: TestParseCommentOnColumn, 2- & 3-part),
internal/testport/pgdump_connsetup_test.go (fixture: COMMENT ON TABLE/COLUMN +
slice-55 assertions + header note),
docs/design/0110-0001-pg-dump-tap-port.md (slice 55 entry + guard list).
Gates: gofmt clean (my files); vet clean; parser+catalog suites PASS;
TestParseCommentOnColumn PASS; TestPort_PgDumpConnectionSetup PASS (exit-0, both
TABLE+COLUMN comments round-trip); pgbench CI-parity smoke via pre-commit hook.

=== NEXT STEP — DU-002 slice 56 ===
Enrich the fixture further to find the next REAL pg_dump gap. Candidates:
  - COMMENT ON CONSTRAINT / INDEX round-trip (SetComment paths exist; verify the
    3-part / `ON table` parse + pg_dump emit). Likely quick win or small gap.
  - other reloptions beyond fillfactor (autovacuum_*, toast.*) — only fillfactor
    is parsed into With from CREATE TABLE; others discarded.
  - SEQUENCE / serial column — sequences skipped from pg_class virtual view
    (Virtual && no View), so getTables never sees relkind='S'; larger slice.
Known orthogonal pre-existing: plpgsql user functions can't be dumped (plpgsql
absent from pg_language → prolang=0 → dumpFunc join 0 rows). Also: the server
SILENTLY SWALLOWS parse errors on COMMENT statements (fallback no-op) — latent
gotcha if a future COMMENT form is malformed; not fixed (scope).

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (CLOG).

NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop; Edit goes stale).
RUN TestPort_PgDumpConnectionSetup after each fixture add to find the REAL blocker.
