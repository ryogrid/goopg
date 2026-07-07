Task: M0097-0150 follow-up 2 of 2 (deferral ledger resume point) — `ALTER
FUNCTION ... SET config_param = value1, value2` (comma-separated `var_list`
form) now parses. COMPLETE and committed this loop.

Files: internal/parser/ddl.go (ALTER FUNCTION SET branch, ~line 7302: value
consumption now calls the existing `p.parseSetValueAtoms()` helper instead
of `p.advance()`-ing a single token — reuses the same atom parser the
generic `SET` statement's `parseSet`/`parseSetValue` already use for
comma-separated GUC values; result discarded, still a no-op).
internal/parser/alter_function_owner_schema_test.go (2 new cases in the
existing TestParseAlterFunctionGenericSetReset table). .ralph/deferral_ledger.md
(flipped both open 2026-07-08 M0097-0150 rows to resolved; appended a NEW
"CRITICAL" row — see Findings below). .ralph/fix_plan.md (M0097-0150
follow-up write-up; M0122-0007's "Still open" trimmed; new URGENT
M0119-0006-DATCONNLIMIT-DEFAULT bullet added ahead of M0119-0007).
docs/design/0015-0002-pg-proc-catalog-and-routine-registry.md +
docs/design/README.md (ALTER FUNCTION cluster marked "no known open
residuals").

Key symbols: internal/parser/ddl.go's ALTER FUNCTION SET branch (~7278-7307);
internal/parser/parser.go's parseSetValueAtoms (~2824, reused, unchanged).

Findings: (1) Comma-list fix verified non-vacuous via `git stash` on ddl.go
alone (both new cases fail pre-fix with syntax error at the comma). Live
e2e against a real cmd/goopg binary (superuser `postgres` session) confirmed
both forms parse+execute as `ALTER FUNCTION`, function stayed callable.
M0097-0150's whole ALTER FUNCTION/PROCEDURE/ROUTINE cluster now has no known
open residuals. (2) **MAJOR discovery while e2e-verifying as a NON-superuser
role** ("alice", freshly CREATE ROLE'd): a real, severe, previously
undetected bug — `catalog.InMemory.DatabaseConnLimit(name)` returns the Go
zero-value `0` for any database that never had `SetDatabaseConnLimit`
called (i.e. EVERY database in a fresh cluster — nothing seeds it at
CREATE DATABASE time). Real PG's default is `-1` (unlimited); `0` means
"reject everyone". M0119-0006's already-landed positive-datconnlimit
enforcement (`internal/server/server.go` ~line 959) does `limit >= 0 &&
!isSuperuserRoleName(user) && count > limit` — with the buggy `0` default
this rejects literally the FIRST connection any non-superuser role ever
makes to any database, with `too many connections for database %q`.
Live-reproduced: `psql -U postgres -c "CREATE ROLE alice LOGIN;"` then
`psql -U alice -d postgres -c "SELECT 1;"` → FATAL too many connections;
`SELECT datname, datconnlimit FROM pg_database` shows `0` for
postgres/template0/template1 (should be `-1`). Zero test coverage — every
existing datconnlimit test (`internal/server/database_exists_test.go`)
connects as the hardcoded superuser name `"postgres"` (`isSuperuserRoleName`
is a literal string match), which bypasses the check entirely. Deliberately
NOT fixed this loop (unrelated to the ALTER FUNCTION task, one-task-per-loop
discipline) — filed as its own deferral ledger "CRITICAL" row + fix_plan
M0119-0006-DATCONNLIMIT-DEFAULT bullet (marked URGENT, placed directly
before M0119-0007 in the M0119 list) with the exact fix (comma-ok map
lookup returning -1 for unset, fix the stale "0 = no limit" comment at
catalog.go:2059-2060, check pg_database's VirtualRows builder for the same
wrong-default assumption on the SELECT-side render, add a regression test
for a fresh non-superuser role's first connection succeeding).

Next step: **pick M0119-0006-DATCONNLIMIT-DEFAULT next** (fix_plan.md, URGENT
marker) — it is small, well-scoped, and severe (blocks basic non-superuser
connectivity cluster-wide in real usage, though masked in every gate script
because they all connect as "postgres"). Exact fix in the ledger's new
CRITICAL row and fix_plan bullet. After that: M0119-0004 (pg_dump, blocked on
per-DB catalog isolation — soft warning only) / M0119-0005 (needs hash/gin/
gist/spgist/brin AMs, large) / M0119-0006's opclass-dispatch remainder (large,
index-AM-level) / M0119-0007 (blocked on logical decoding) remain the other
open M0119 items, all larger than a single loop.

Gates run: go build ./... clean; go vet ./internal/parser/... clean. go test
./internal/parser/... ./internal/executor/... ./internal/wal/...
./internal/initdb/... ./internal/planner/... ALL PASS.
scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33). RALPH_PRECOMMIT_SCOPE=smoke
scripts/ralph-precommit-test.sh PASS (0 failed, all 3 workloads) — run
standalone AND again automatically via the .githooks/pre-commit hook at
commit time. Live e2e: real cmd/goopg binary on 127.0.0.1:65499 — both new
SET var_list forms confirmed correct parse+execute as superuser; separately
used the same server to reproduce the datconnlimit bug as a non-superuser
role (see Findings). make ralph-state-guard: 1 benign issue auto-repaired
(identical pattern to every prior loop — status/progress clean-exit-vs-
in_progress reconciliation).

In-flight: none. Scratch server/binary/data dir
(/tmp/goopg-bin-setlist, /tmp/goopg-alterfunc-setlist-verify,
/tmp/goopg-setlist-server.*) were fully torn down (server killed, files
removed) before this loop ended.
