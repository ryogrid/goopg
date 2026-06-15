Task: M0095-0003 (011_in_place_tablespace) — loop #12. Landed the in-place
tablespace FOUNDATION. The tree is now CLEAN (the foreign gen-column WIP that
hard-blocked parser/executor/catalog edits for 15+ loops is gone), which
unblocked this.

=== WHAT LANDED (this loop) ===
allow_in_place_tablespaces GUC (PGC_SUSET, boot off) + CREATE/DROP TABLESPACE
DDL end-to-end:
- config/defaults.go + postgresql.conf.sample (DEVELOPER OPTIONS section)
- parser/ast.go: CreateTablespaceStmt, DropTablespaceStmt
- parser/ddl.go: parseCreateTablespaceTail + DROP TABLESPACE dispatch.
  KEY GOTCHA: `tablespace` is an unreserved KEYWORD (KwTablespace), so dispatch
  must use acceptKeyword(KwTablespace), NOT acceptIdentKeyword (which only
  matches TokenIdent). `owner`/`location` ARE plain idents (acceptIdentKeyword
  ok). `=` is TokenOperator.
- planner/planner.go: both added to DDL passthrough case list
- executor/operators_ddl.go: execCreateTablespace/execDropTablespace — create/
  remove pg_tblspc/<oid> under ctx.DataDir; reads GUC via ctx.GetSetting.
  Upstream-verbatim errors: 42602 quote, 42P17 absolute-path (also empty-loc +
  GUC off), 42939 reserved pg_ name, 42710 dup, 42704 missing; external
  absolute LOCATION → 0A000 (goopg can't relocate relfiles).
- catalog/catalog.go: tablespaces registry + CreateTablespace/DropTablespace on
  the Catalog interface (InMemory is the sole implementer).
- server/dispatch.go: CREATE/DROP TABLESPACE command tags.

Tests: parser (3), catalog (1), executor (7, incl. real-temp-dir create/drop),
config (1) — all PASS. Suites parser/catalog/config/planner/executor/server
green; gofmt+vet+build clean. TPC-H spotcheck SKIPPED (no data dir; safe by
construction — only new DDL statement types added to passthrough). Design
docs/design/0095-0003-in-place-tablespace.md + README index (status: partial).

=== NEXT STEP (resume) — 011 remainder ===
011_in_place_tablespace.pl still self-skips on BASE_BACKUP. To make it pass:
(a) internal/server/basebackup.go must enumerate in-place tablespaces and emit
    one <oid>.tar each (pg_basebackup -T relocation on restore);
(b) create the pg_tblspc/<oid>/PG_18_<catversion> version subdir in
    execCreateTablespace (faithful to create_tablespace_directories) — needs the
    catversion string, single source of truth in internal/initdb
    (pgCatalogVersionNo=202506291, CatalogVersion="18"); land (a)+(b) together.
On-disk pg_tablespace heap visibility = separate shared-catalog-write capability
(no RelFileNode resolver for shared pg_tablespace); defer, not needed by 011.

NOTE: tree is clean now — other long-deferred items (M0110-0003 AC-003 needs
index AMs/types/opclass; M0117 Part B = Effort-L CLOG memory-model, full-gate)
remain the genuinely large ones.
