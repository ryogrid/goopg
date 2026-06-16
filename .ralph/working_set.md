Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 48 COMPLETE
(committed this loop). NEXT loop starts on slice 49. NOTHING in flight after commit.

=== DONE (loop #3) — DU-002 slice 48 (column type-modifier fidelity / atttypmod) ===
After slice 47 the plain-table dump was byte-identical to upstream. Enriched the
fixture with `amount numeric(10,2)` + `code character varying(8)` and found the
next gap: both dumped as their BARE base type (numeric, character varying).
Root cause: buildUserPGAttributeRow (internal/executor/pg18_user_catalog_rows.go)
hardcoded atttypmod=-1, discarding catalog.Type.Args. pg_dump getTableAttrs reads
format_type(atttypid, atttypmod), so -1 → unmodified type.
Fix: new pgAttTypmod(typOID, args) computes PG-canonical atttypmod (numeric:
((p<<16)|s)+VARHDRSZ; varchar/char: n+VARHDRSZ; else -1), wired into the
atttypmod cell. Decode side: formatTypeOID (internal/executor/expr.go) already
decoded char/varchar length but returned bare numeric → added the numeric branch
(typmod>=4 → numeric(p,s)), mirroring numerictypmodout. Verified empirically:
atttypmod 655366 for numeric(10,2); dump now emits `amount numeric(10,2)` and
`code character varying(8)`.
Files: internal/executor/pg18_user_catalog_rows.go (pgAttTypmod + wire),
internal/executor/expr.go (formatTypeOID numeric decode),
internal/executor/pg18_user_catalog_rows_test.go (TestUserPGAttributeTypmod),
internal/testport/pgdump_connsetup_test.go (richer fixture + assertions + slice-48
header), docs/design/0110-0001-pg-dump-tap-port.md (slice 48 section).
Gates: gofmt/build clean; executor PASS; catalog+planner PASS;
TestPort_PgDumpConnectionSetup PASS (asserts numeric(10,2)+varchar(8) round-trip);
TestPort_PgDump* + Psql001Basic PASS; ralph-state-guard OK. pgbench CI-parity
smoke runs in the pre-commit hook.

=== NEXT STEP — DU-002 slice 49 ===
Re-run TestPort_PgDumpConnectionSetup with an even richer fixture to find the next
schema-fidelity gap. Candidates to probe: PRIMARY KEY / UNIQUE constraints
(ALTER TABLE ADD CONSTRAINT emission), CHECK constraints, a real reloptions table
(WITH (fillfactor=70) — now that the empty-array case is fixed, a real reloption
should pass through), a second schema, a SEQUENCE/serial column, foreign keys.
RUN the TAP test first — it finds the REAL next blocker. Known orthogonal
pre-existing: plpgsql user functions can't be dumped (plpgsql absent from
pg_language → prolang=0 → dumpFunc join 0 rows).

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (CLOG).
