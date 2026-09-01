(idle — nothing in flight)

## Loop #13 (2026-09-01) result — M0119-0006 97th slice landed (commit `56e4f98f6`)

**Nightly triage:** `ci/logs/action-items.md` still the same run
`20260901-010436` (7 items, mtime 02:11) loops #10-#12 already confirmed
filed. Re-verified this loop: all 7 subjects (`IsolationIntraGrantInplace`,
`IsolationStats`, `LockRowsSortOverJoinTakesRowLock`, `PgDumpConnectionSetup`,
`PgStatActivity`, `RegressSuite`, `TestSyntax_Catalog_PgStatActivity`) already
have an unchecked M-NIGHTLY task in `fix_plan.md` from earlier runs. No new
filing needed.

**Task:** per the Current Priority banner (M0134 exhausted, active selection
M0119, sole open task M0119-0006 "pg_amcheck server tier"). Rather than
trusting the CSV's stale "unsupported index AM" premise, empirically drove
the REAL `pg_amcheck` binary against `003_check.pl`'s exact upstream fixture
(box/int4range/int4[] columns + `USING {BTREE,HASH,BRIN,GIST,GIN,SPGIST}`
indexes) on a fresh capped goopg server. Finding: the **entire fixture setup
already succeeds** — refutes half the AC-003 premise (mirrors the earlier
`[[ac003_blocker3_refuted_pg_amcheck_whole_db_clean]]` pattern). What
`pg_amcheck` itself surfaced instead: `pg_class.relam` reported btree (403)
for EVERY gist/gin/spgist/brin index, so pg_amcheck's own btree-only
enumeration query wrongly selected them and errored "is not a B-Tree index"
(exit 2, six spurious errors).

**Root cause:** two sibling pg_class row builders — `internal/catalog/
catalog.go`'s virtual `VirtualRows` pg_class path and `internal/executor/
pg18_user_catalog_rows.go`'s `buildUserPGClassRowForIndex` (heap-persisted,
PG-standby-facing sibling) — only special-cased `idx.Method=="hash"` (itself
DEAD: a `USING hash` index stores `idx.Method=="btree"` by design,
`idx.DeclaredHash` is the separate marker) and silently defaulted every other
non-btree method to btree's oid. gist/gin/spgist/brin indexes ARE registered
catalog-only under their real `Method` string (`execCreateIndex`'s
`method=="gist"||...` branch) — the builders just never consulted it, even
though the canonical `AccessMethodOIDByName(name)` map already existed one
file away.

**Fix landed:** both builders now resolve `AccessMethodOIDByName(idx.Method)`,
carved out for `idx.DeclaredHash` (hash unchanged — still reports btree's
oid, matching the documented "everywhere else in goopg" contract). New
`indexRelamOID` helper in `pg18_user_catalog_rows.go`. Verified end-to-end:
`pg_amcheck --schema=s1 postgres` went from exit 2 to exit 0/clean, identical
to real PG.

**Files:** `internal/catalog/catalog.go` (relam builder fix), `internal/
executor/pg18_user_catalog_rows.go` (`indexRelamOID` helper + call site),
`docs/design/0100-0149/0119-0006-pg-class-relam-nonbtree-index-am.md` (new
design doc, accepted), `docs/design/README.md` (indexed as `0119-0006bm`),
`.ralph/deferral_ledger.md` (new row).

**Key symbols:** `catalog.AccessMethodOIDByName`, `catalog.Index.Method` /
`.DeclaredHash`, `executor.indexRelamOID`, `executor.execCreateIndex`
(operators_ddl.go:7502, the gist/gin/spgist/brin catalog-only branch at
:7632).

**New deferral (recorded, NOT fixed — separate bug, one-task-per-loop):**
while re-verifying, an EMPTY partition child (`CREATE TABLE p1_1 PARTITION OF
p1 FOR VALUES IN (...)`, zero rows ever inserted) makes `pg_amcheck` error
`could not open file ... No such file or directory` on `verify_heapam()` —
goopg apparently never creates a heap relation's main-fork file until first
write (lazy `smgr` creation), so a genuinely-empty table looks ENOENT the
same way a REMOVED file does (see `[[goopg_smgr_ocreate_recreates_removed_files]]`).
Root-causing this (partition-specific vs. general) is the next M0119-0006
slice — resume point and the "must not regress
`TestVerifyHeapam_DetectsMissingRelationFile`" constraint are in the ledger
row appended this loop.

**NEXT LOOP:** Re-check the `## Current Priority` banner first (still M0119
unless something changed it). Continue M0119-0006 with the empty-heap-file
ENOENT deferral above as the concrete next slice: (1) confirm whether it's
partition-specific or general (repro with a bare non-partition empty table);
(2) if general, decide fix site — eager file touch at CREATE TABLE time vs.
teaching `verify_heapam`'s Open/NBlocks path (`internal/executor/
operators_verify_heapam.go`) to disambiguate "empty, never written" from
"removed after having data" without breaking
`TestVerifyHeapam_DetectsMissingRelationFile`. Continue driving the REAL
`pg_amcheck` binary against fixture slices — it is a much faster oracle for
this milestone than reasoning from the CSV's (partially stale) blocker
descriptions.

**Gates run:** `go build ./...` clean; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` full suite PASS; `scripts/tpch-spotcheck.sh`
PASS (Q12=2, Q13=34); pre-commit pgbench smoke PASS (509/655/11768 TPS, 0
failed) fired automatically via the git hook. `make ralph-state-guard`:
found the same benign running/completed mismatch loops #4-#13 have all seen,
auto-repaired.

**In-flight:** none. Throwaway probe server/data dir (`/tmp/m0119data`,
`/tmp/goopg-m0119`) cleaned up before commit.
