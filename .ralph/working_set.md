Task: M0097-0150 follow-up (fix_plan M0122-0007 bucket) — fixed the generic
`ALTER FUNCTION ... SET config_param {TO|=} value` / `SET config_param FROM
CURRENT` / `RESET` parsing gap the previous loop's deferral ledger row
handed off. COMPLETE and committed+pushed this loop (a96c07ed).

Files: internal/parser/ddl.go (ALTER FUNCTION attribute loop's `SET` branch,
~line 7277: widened `=`-acceptance to `(TokenSymbol||TokenOperator) &&
Value=="="` matching the file's established idiom; restructured so the
config-param name parses BEFORE checking TO/=/FROM CURRENT, per real
gram.y's `var_name {TO|=} var_value | var_name FROM CURRENT` order — old
code checked for a literal "from" token immediately after SET, treating
FROM as if it could be the param name), internal/parser/alter_function_owner_schema_test.go
(new TestParseAlterFunctionGenericSetReset, 7 cases). .ralph/deferral_ledger.md
(flipped the prior M0097-0150 row to resolved; appended a new row recording
this fix + the newly-discovered comma-separated-value-list gap).
.ralph/fix_plan.md (M0122-0007 follow-up write-up + "Still open" trimmed).
docs/design/0015-0002-pg-proc-catalog-and-routine-registry.md +
docs/design/README.md (updated the "Still open" trailer to "done" +
narrower remaining gap).

Key symbols: the ALTER FUNCTION/PROCEDURE/ROUTINE attribute-consuming loop
in internal/parser/ddl.go's parseAlterStmt (or sibling) — the `SET` branch
right after the OWNER TO/RENAME TO/SET SCHEMA sub-branches, ~line 7239-7300.

Findings: (1) fix verified non-vacuous via `git stash` on ddl.go alone —
3/7 new test cases fail pre-fix with the exact predicted syntax errors
(SET x = v, SET x FROM CURRENT, IMMUTABLE SET x = v). (2) Live e2e against
a real cmd/goopg binary confirmed all 5 previously-broken forms (SET x TO v,
SET x = v, SET x FROM CURRENT, SET x TO DEFAULT, RESET x, RESET ALL) now
parse+execute as `ALTER FUNCTION` (all no-ops, goopg has no per-function
GUC-override storage — matches pre-existing RESET behavior). (3) Also
discovered LIVE (not just reasoned about) a narrower, pre-existing,
NOT-introduced-by-this-fix gap: `SET config = value1, value2` (comma-
separated value list, real PG var_list grammar for list-valued GUCs like
search_path) still errors — the no-op branch only ever consumes ONE value
token, unchanged from the original code. Recorded as its own resume point
in the deferral ledger (2nd 2026-07-08 M0097-0150 row) rather than fixed
this loop, since it's additive scope (a list form never worked, not a
regression) and the loop's actual named handoff (the = and FROM-CURRENT
bugs) was already complete.

Next step: pick the next task. M-NIGHTLY re-verified clean at this loop's
start (ci/logs/action-items.md's run 20260707-000712, all 8 AI- items
already [x] in fix_plan.md — including the massive 17-loop pgbench/nightly
keyLen-mismatch investigation, which WAS fully root-caused and fixed
(flushBatch stale-tag bug, bufpool.go) and confirmed with 0 failures on
back-to-back authoritative repros; nothing new to triage). Candidates
carried forward: (a) the just-recorded comma-separated SET-value-list gap
(small, ddl.go ~7290, loop `,`-value pairs after the first value token —
see deferral ledger 2nd 2026-07-08 M0097-0150 row for the exact resume
point); (b) M0122-0006's opclass/collation OID resolution gap (indclass/
indcollation real OID resolution, live AND heap-restore paths — flagged
"defer indefinitely", large: needs a full builtin-opclass-name registry);
(c) M0122-0007's remaining ~11 items: CREATE/DROP DATABASE full DDL
(architecturally large — goopg is single-database today), REINDEX physical
rebuild (validation/locking already real, just no-op rebuild), tablespaces,
`ALTER TABLE ... ALTER COLUMN RESET (...)`, planner/jit GUC stubs;
(d) M0122-0008 (SASLprep/channel binding/scram_iterations; RBAC mostly
done, view's-own-ACL gap remains); (e) M0119-0004/0005/0006/0007 per the
Current Priority banner (M0119-0005 hash/gin/gist/spgist/brin AM gap,
M0119-0006 pg_amproc dispatch gap — check overlap with candidate (b)'s
opclass work before picking both).

Gates run: go build ./... clean; go vet ./... clean (only pre-existing,
unrelated staticcheck hints on other files, not touched by this loop's
diff). go test ./internal/parser/... ./internal/executor/... ./internal/wal/...
./internal/initdb/... ./internal/planner/... ALL PASS.
scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33). RALPH_PRECOMMIT_SCOPE=smoke
scripts/ralph-precommit-test.sh PASS (0 failed, all 3 workloads) — run
standalone AND again automatically via the .githooks/pre-commit hook at
commit time. Live e2e: real cmd/goopg binary on 127.0.0.1:65498 — CREATE
FUNCTION + all 6 new ALTER FUNCTION SET/RESET forms, confirmed correct
parse (no syntax error) and function stayed callable
(app-free SELECT add_one(41)=42). make ralph-state-guard: 1 benign issue
auto-repaired (identical pattern to every prior loop — status/progress
clean-exit-vs-in_progress reconciliation).

In-flight: none. Manual verification data dir (/tmp/goopg-alterfunc-set-verify)
and scratch binary (/tmp/goopg-bin-setfix) were fully torn down (server
killed, files removed) before this loop ended. Committed a96c07ed, pushed
to origin/align-data-structure-with-pg.
