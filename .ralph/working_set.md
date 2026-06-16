Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 57 COMPLETE
(committing this loop). NEXT loop starts on slice 58.
NOTHING in flight after commit.

=== DONE (loop #12) — DU-002 slice 57 (VIEW round-trip via pg_get_viewdef) ===
Real gap: a single user VIEW made pg_dump ABORT THE WHOLE DUMP —
`definition of view "v" appears to be empty (length zero)`. pg_dump's
createViewAsClause calls pg_get_viewdef(oid), which goopg stubbed to NULL.
Side effect: table DATA emitted after the view never appeared either.
FIX (raw-text capture, NOT a full deparser):
 - parser: parser struct keeps the original source string (p.src, set in
   Parse). New captureSrcSpan(startPos, endTok) slices src text, trims
   whitespace + trailing ';'. parseCreateViewTail stores the view body span on
   CreateViewStmt.RawDef (excludes the trailing WITH CHECK OPTION clause).
 - catalog.Table: new ViewDef string field.
 - executor execCreateView: copies s.RawDef → vt.ViewDef (CreateView returns the
   fresh table for both CREATE and CREATE OR REPLACE).
 - executor pg_get_viewdef (expr.go): resolves arg as OID (pg_dump) or name
   (psql) → view → returns ViewDef + ";". pg_dump Asserts last char is ';',
   strips it, wraps body in `CREATE VIEW … AS <body>`.
Files: internal/parser/parser.go (src field + captureSrcSpan + p.src=input),
internal/parser/ddl.go (parseCreateViewTail capture),
internal/parser/ast.go (CreateViewStmt.RawDef),
internal/catalog/catalog.go (Table.ViewDef field),
internal/executor/operators_ddl.go (execCreateView stores ViewDef),
internal/executor/expr.go (pg_get_viewdef impl),
internal/parser/view_test.go (NEW TestParseCreateViewRawDef),
internal/testport/pgdump_connsetup_test.go (CREATE VIEW fixture + slice-57 asserts + header),
docs/design/0110-0001-pg-dump-tap-port.md (slice 57 entry + guard paragraph).
FIDELITY GAP (documented/deferred): raw text is NOT schema-qualified — PG's
deparser qualifies unqualified relation refs. Qualified views (fixture uses
public.foo) restore cleanly under pg_dump's search_path=''; an unqualified view
would fail to restore. RECURSIVE views + materialized views capture no RawDef
yet (different parser paths) — follow-up.
Gates: gofmt clean (touched files); vet clean (parser+executor); parser +
catalog + executor + initdb suites PASS; new TestParseCreateViewRawDef PASS;
TestPort_PgDumpConnectionSetup PASS (exit-0, view round-trips, DATA emits);
pgbench CI-parity smoke via pre-commit hook.

=== NEXT STEP — DU-002 slice 58 ===
Enrich the fixture to find the next REAL pg_dump gap. Candidates probed/known:
  - VIEW with renamed columns: CREATE VIEW v(a,b) AS … — RawDef body lacks the
    aliases, so pg_dump would emit wrong column names. Likely a real gap.
  - MATERIALIZED VIEW round-trip (no RawDef captured yet; needs its own path).
  - SEQUENCE / serial column — sequences skipped from pg_class virtual view
    (Virtual && no View), so getTables never sees relkind='S'; larger slice.
  - Column DEFAULT expression (DEFAULT now()) — VERIFIED WORKS (slice-57 probe).
ALWAYS: add ONE fixture element, run TestPort_PgDumpConnectionSetup, inspect the
actual dumped output (temporary PROBE t.Logf), confirm whether goopg already
handles it before assuming a gap.
Known orthogonal: plpgsql user funcs can't be dumped (plpgsql absent from
pg_language). Server SILENTLY SWALLOWS parse errors on COMMENT/DDL stmts.

NOTE: do NOT Edit .ralph/fix_plan.md (driver churns it mid-loop; Edit goes stale).
