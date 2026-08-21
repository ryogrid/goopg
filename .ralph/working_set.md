Task: M0134-0069 (sequence.sql) — landed a real CREATE/ALTER SEQUENCE
UNLOGGED bug this loop (not just the header-text cosmetic it looked like).
Diff shrank 239→226. Case still `failed` (0/1). Committed & pushed
(a61c7b46 impl, f2b7aa7c bookkeeping).

Files this loop: `internal/parser/ast.go` (new `CreateSequenceStmt.Unlogged`,
`AlterSequenceStmt.SetLogged` fields), `internal/parser/ddl.go`
(`parseCreateSequenceTail` gained a 3rd `unlogged bool` param — was
conflated into the `temp` param before; ALTER SEQUENCE SET LOGGED/UNLOGGED
option-parsing now stores instead of discarding), `internal/executor/
operators_ddl.go` (`execCreateSequence`/`execAlterSequence` now set
`catalog.Table.Unlogged` so `buildUserPGClassRow`'s existing
relpersistence logic picks it up), `internal/executor/
sequence_unlogged_test.go` (new, 2 tests). Also updated
`.ralph/fix_plan.md` (M0134-0069 entry, no new ledger row needed — this
was a full landing, not a deferral).

Key symbols: `parseCreateSequenceTail` (internal/parser/ddl.go),
`execCreateSequence`/`execAlterSequence` (internal/executor/
operators_ddl.go), `catalog.Table.Unlogged`, `buildUserPGClassRow`
(internal/executor/pg18_user_catalog_rows.go).

Hypothesis/Findings: a researcher audit (delegated before implementing)
refuted the working_set's prior "new SESSION AUTHORIZATION ACL gap"
hypothesis — it's the SAME already-ledgered Bucket 7 blocker (GRANT/REVOKE
inside BEGIN...ROLLBACK never reaching the catalog; autocommit-only gate
in internal/postmaster/query.go), not new plumbing; `ctx.NonSuperuserRole`
is already correctly threaded from SET SESSION AUTHORIZATION into all four
sequence ACL checks. The SAME audit found the `\d` UNLOGGED-header gap was
NOT cosmetic: `CREATE UNLOGGED SEQUENCE` was silently mis-flagging the
sequence as session-TEMPORARY (both booleans shared one param), and ALTER
SEQUENCE SET LOGGED/UNLOGGED was a parsed-then-discarded no-op. Fixed both;
confirmed live via cgroup-capped psql that `\d` now matches PG's
describe.c:1857-1861 header logic. `information_schema.sequences.
sequence_catalog` DB-name mismatch confirmed to be a harness artifact (
pg-regress-runner.sh connects to DB "postgres" not "regression"), not an
engine bug — no fix needed, just documented.

Next step: two paths sized by the researcher audit, pick one: (a) Bucket 7
— transactional GRANT/REVOKE deferred-catalog-mutation plumbing (NOT a
small fix per the audit; needs a design pass reconciling
relACLEmptied/relACLOwnerRevoked's existing display-only semantics with a
new enforcement consumer — mirror the execCompatNoop xmax/lock deferral
pattern per the ledger's Bucket-7 rows) — or (b) `pg_sequence_parameters()`
SRF as a standalone smaller slice (currently `table-valued function ...
not supported`). Recommend researching (b) first since it's likely smaller
and self-contained, then tackle (a) as its own design-doc-backed task. If
the banner has moved off M0134, re-check `.ralph/fix_plan.md`'s Current
Priority banner before continuing.

Gates run this loop: `go build ./...` PASS; `go test ./internal/parser/...
./internal/executor/...` PASS (implementer, no -count=1); live cgroup-
capped psql probe confirmed \d header parity; `scripts/pg-regress-
runner.sh --verbose sequence` 226 lines (down from 239), zero
unlogged-related hunks remain; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS (tester agent, ~8 min, internal/
initdb cold-build dominated); `make ralph-state-guard` — found 2 stale
markers (prior loop's clean-exit marker), auto-repaired, then PASS;
pre-commit pgbench smoke PASS x2 (both commits, ~355-360 TPS TPC-B,
~657-661 TPS simple-update, ~11800-11900 TPS select-only, 0 failed).

Delegation: researcher agent `a7dd22b1ef13901e3` (sized the SESSION
AUTHORIZATION gap, refuted it as new, found the real UNLOGGED bug);
implementer agent `a9b3694126b3f8da5` (1 round — landed parser+executor
fix + tests cleanly, no deviations); tester agent `a186792720efb2d91` (1
round — confirmed units gate PASS).

In-flight: none. Commits `a61c7b46` and `f2b7aa7c` pushed to
`regress-renumbering`. No server left running.
