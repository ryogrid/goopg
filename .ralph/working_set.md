Task: M0134-0069 (sequence.sql) — Bucket 5 now FULLY landed (sequence ACL/owner
enforcement). Case still `failed` (0/1, diff 286→275 lines this loop).
Committed & pushed (08b403d1).

Files this loop: `internal/executor/operators_sequence.go` (new
`resolveSeqCatalogTable` helper; `evalNextval`/`evalCurrval`/`evalSetval`/
`evalLastval` each now require a privilege before acting), `internal/executor/
operators_storage.go` (new `dmlPrivilegePermittedAsAny` OR-wrapper delegating
to `dmlPrivilegePermittedAs`), `internal/executor/operators_ddl.go`
(`execAlterSequence` owner check via `checkCommentObjectOwner`;
`createSeqCatalogTable` now stamps `Owner` at sequence creation — was always
empty before), `internal/executor/sequence_acl_test.go` (5 new tests),
`.ralph/deferral_ledger.md` (new row, M0134-0069 dated 2026-08-21 — fourth
entry), `.ralph/fix_plan.md` (M0134-0069 entry updated, still unchecked).

Key symbols: `resolveSeqCatalogTable`, `dmlPrivilegePermittedAsAny`
(operators_storage.go), `execAlterSequence`'s new owner-check block,
`createSeqCatalogTable`'s new `Owner` stamp.

Hypothesis/Findings: case still `failed` overall — 5 of 6 sizing buckets now
fully landed (1, 2, 3, 4, 5). A broader gap was discovered and deliberately
LEFT UNFIXED this loop (per the brief's own scope boundary, escalated not
patched around): `dmlPrivilegePermittedAs`'s owner-bypass
(`operators_storage.go:2238-2240`) is unconditional — PG's owner privilege is
actually a revocable implicit aclitem (`REVOKE ALL ON seq FROM
<owning-role>` denies the owner too), and `sequence.sql:645-786`'s
REVOKE-then-selective-GRANT sub-block needs that. This fix is TABLE-WIDE (used
by every DML/SELECT/TRUNCATE owner-bypass check), not sequence-specific —
needs its own sizing/design pass before briefing, and must first confirm no
currently-green test (e.g. `TestDMLRequiresTablePrivilege`) depends on the
current unconditional-bypass semantics. Groundwork exists:
`internal/catalog/catalog.go`'s `aclOwnerRole` sentinel +
`relACLOwnerRevoked`/`relACLEmptied` bookkeeping (`RevokeTablePrivilege`,
catalog.go:16346-16371) already tracks this for ACL-*display* purposes
(`\dp`, pg_dump relacl) — only the *enforcement* consult is missing.

Remaining M0134-0069 buckets/items per the ledger: Bucket 6 (small
text/HINT/DETAIL gaps), `pg_sequence_parameters()` SRF missing, `\d` doesn't
label UNLOGGED sequences, orphaned `sequence_test2` catalog row after
cascading DROP TABLE, `pg_get_sequence_data` cache-vs-persisted mismatch,
CASCADE-mode column-DEFAULT cascade drop, and the new owner-ACL-revocation
gap above.

Next step: size and decide between (a) Bucket 6 (small text gaps — likely
fastest remaining win) or (b) the owner-ACL-revocation table-wide fix (larger,
higher-risk, but may be needed to fully close sequence.sql's privilege
sub-block regardless of Bucket 6). Recommend delegating a researcher round
first to size Bucket 6's line-count impact vs. the owner-ACL-revocation gap's
remaining diff-line impact, so the next loop picks the higher-leverage one.

Gates run this loop: `go build ./...` PASS; targeted test PASS (5/5 new
tests); full `go test ./internal/executor/...` PASS (no regressions);
`scripts/pg-regress-runner.sh --verbose sequence` — diff 286→275, the ALTER
SEQUENCE-as-non-owner anchor now matches PG byte-for-byte;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS (full
suite incl. slow initdb/goopg packages, ~470s, run twice to confirm no
flakiness); `make ralph-state-guard` — found 2 stale markers, auto-repaired,
then PASS; pre-commit pgbench smoke PASS (11714 TPS select-only, 649 TPS
simple-update, 353 TPS TPC-B, 0 failed).

Delegation: researcher agent `ad828c8810a35bd55` (1 round — sized Bucket 5,
confirmed PG semantics + reuse path, no escalation); implementer agent
`a375f50ed827527aa` (1 round — landed the full brief, escalated the
owner-ACL-revocation gap per its own escalation trigger rather than patching
around it, no re-brief needed); tester agent `a3a63024eefe6f89f` (1 round —
ran the full units pre-commit gate twice, PASS both times).

In-flight: none. Commit `08b403d1` pushed to `regress-renumbering`. No
server left running.
