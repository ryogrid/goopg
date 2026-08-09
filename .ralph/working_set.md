Task: M0119-0004 — DU-002 next blocker: ALTER SERVER OWNER TO

Files:
- internal/parser/ddl.go: added ALTER SERVER dispatch in parseAlter() (after ALTER FOREIGN DATA WRAPPER, before ALTER FOREIGN TABLE). Detects `server` ident keyword, consumes server name + trailing clauses (including OPTIONS with balanced parens), returns CompatNoopStmt with Tag="ALTER SERVER", ObjType="server".
- internal/parser/op_compat_test.go: added TestParseAlterServerOwner (OWNER TO, OWNER TO CURRENT_USER, OPTIONS, VERSION forms)

Key symbols:
- parseAlter: new `if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "server")` block
- execCompatNoop: NO CHANGE needed — existing `case "server":` idempotently handles ALTER SERVER (RegisterForeignServer merges only non-empty fields, all empty for ALTER)

Hypothesis/Findings:
- ALTER SERVER OWNER TO was the parseAlter DU-002 blocker from the previous loop. FIXED.
- ALTER FOREIGN DATA WRAPPER OWNER TO: already handled by the existing token-skipping default case in the parseAlter FDW branch.
- ALTER EVENT TRIGGER OWNER TO: already handled explicitly (ddl.go:8064-8080).
- Next blocker: `NOT NULL pid` syntax error in CREATE TABLE with INHERITS — pg_dump emits bare `NOT NULL pid` column constraint for inherited NOT NULL columns. Parser fails with "expected identifier (got not)" at the NOT keyword. This is a different class of gap (column-constraint parsing in CREATE TABLE INHERITS body), not another ALTER OWNER TO.

Next step:
Fix the "NOT NULL pid" column-constraint parsing gap in CREATE TABLE INHERITS. The NOT NULL constraint is emitted as a standalone column constraint item in the table body (not part of the column definition), and the parser doesn't recognize it in that position.

Gates run:
- go build ./...: PASS
- go test ./internal/parser/...: PASS (0.045s)
- go test ./internal/executor/...: PASS (5.809s)
- TestPort_PgDumpConnectionSetup: PASS (3.68s) — ALTER SERVER assertion passed; round-trip fails at next blocker (NOT NULL pid)
- RALPH_PRECOMMIT_SCOPE=units: PASS (all packages)
- RALPH_PRECOMMIT_SCOPE=smoke: PASS (0 failed, all workloads)
- make ralph-state-guard: REPAIRED + OK

In-flight: none
