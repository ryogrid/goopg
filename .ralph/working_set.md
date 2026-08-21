Task: M0134-0069 (sequence.sql) — Bucket 6 item 4 landed this loop. Case
still `failed` (0/1); diff shrank 265→253 lines. Committed & pushed
(542eb1ec).

Files this loop: `internal/executor/operators_ddl.go` (`validateSeqOwnedBy`
— index-lookup fallback), `internal/executor/sequence_acl_test.go` (new
`TestValidateSeqOwnedByIndexTarget`). Also updated `.ralph/fix_plan.md`
(M0134-0069 entry) and `.ralph/deferral_ledger.md` (new row, ninth
M0134-0069 entry).

Key symbols: `validateSeqOwnedBy` (`internal/executor/operators_ddl.go`).

Hypothesis/Findings: Bucket 6 item 4 (OWNED BY naming an index →
42809+DETAIL) is done, matches PG oracle
`postgres/src/backend/commands/sequence.c:1629-1638`
(`process_owned_by`)+`errdetail_relkind_not_supported`
(`pg_class.c:24-52`) byte-for-byte, confirmed live via cgroup-capped psql
probe. Remaining Bucket 6 item 5: `ALTER SEQUENCE`/`nextval`/`currval`/
`setval`/`DROP SEQUENCE` on a non-sequence relation return `42P01 relation
"x" does not exist` instead of PG's `cannot open relation "x"` + relkind
DETAIL under `ERRCODE_WRONG_OBJECT_TYPE` — `LookupSequence` nil conflates
"doesn't exist" with "wrong relkind"; needs an audit of ALL sequence-op
call sites (not just ALTER SEQUENCE) before fixing, since PG centralizes
this in one `validate_relation_kind` choke-point
(`postgres/src/backend/access/sequence/sequence.c:67-75`) but goopg's is
scattered. Also newly visible in the shrunk 253-line diff (untriaged, all
ledgered this loop): `ALTER SEQUENCE ... SET UNLOGGED` doesn't update `\d`
header; ACL enforcement still permits some nextval/currval/setval/lastval
PG denies (Bucket 5/7 territory, transactional-ACL-recording gap already
ledgered); `information_schema.sequences.sequence_catalog` shows connected
DB name not regress DB name (possibly harness artifact, unverified);
`pg_get_sequence_data('test_seq1').last_value` after `CACHE 10` returns 3
not PG's 10 (pre-existing ledgered gap, reconfirmed).

Next step: brief item 5 (relkind disambiguation across sequence-op call
sites) as the next Bucket 6 slice — first have a researcher/implementer
audit which of `execAlterSequence`, `evalNextval`/`evalCurrval`/
`evalSetval`/`evalLastval` (`operators_sequence.go`), and DROP SEQUENCE
share the same `LookupSequence`-nil-conflates-two-cases bug, so the fix can
land as one coordinated slice rather than N single-site patches. If the
banner has moved off M0134, re-check `.ralph/fix_plan.md`'s Current
Priority banner before continuing. After Bucket 6 fully closes, remaining
sequence.sql gaps: `pg_sequence_parameters()` SRF, `\d` UNLOGGED header,
orphaned catalog row after cascading DROP TABLE, `pg_get_sequence_data`
cache mismatch, CASCADE-mode column-DEFAULT drop, transactional
ACL-recording gap (Bucket 7 blocker 1), GrantTablePrivilegeAs
flag-clearing gap (Bucket 7 blocker 2).

Gates run this loop: `go build ./...` PASS; `go test
./internal/executor/...` PASS (implementer, 7.05s); manual cgroup-capped
psql probe confirmed exact PG-faithful error text; `scripts/pg-regress-
runner.sh --verbose sequence` — 253 lines (down from 265); `RALPH_PRECOMMIT_
SCOPE=units scripts/ralph-precommit-test.sh` PASS (tester agent, ~8 min,
initdb+cmd/goopg cold dominated runtime); `make ralph-state-guard` — found
2 stale markers (prior loop's clean-exit marker), auto-repaired, then PASS;
pre-commit pgbench smoke PASS (12107 TPS select-only, 653 TPS
simple-update, 360 TPS TPC-B, 0 failed).

Delegation: implementer agent `a8590e53a1273fa29` (1 round — landed the
fix + test cleanly, no deviations, ran its own manual probe); tester agent
`a791b735d2ee005f3` (1 round — confirmed units gate PASS).

In-flight: none. Commit `542eb1ec` pushed to `regress-renumbering`. No
server left running.
