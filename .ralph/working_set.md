Task: M0134-0069 (sequence.sql) — Bucket 7 (owner-ACL-revocation enforcement)
partially landed this loop, autocommit path only. Case still `failed` (0/1,
diff held at 275 lines — unchanged, the fixture's owner-ACL sub-block runs
inside BEGIN...ROLLBACK which the fix doesn't reach yet). Committed & pushed
(7ff7303c).

Files this loop: `internal/catalog/catalog.go` (new `IsOwnerACLRevoked`/
`HasOwnerPrivilege` Catalog methods), `internal/executor/operators_storage.go`
(`dmlPrivilegePermittedAs`'s owner-bypass now conditional on
`IsOwnerACLRevoked`), `internal/postmaster/grant_ddl.go`
(`tryRecordTableGrant`/`tryRecordTableRevoke` now detect an owner-targeted
GRANT/REVOKE by comparing against `tbl.Owner` instead of the internal
`aclOwnerRole` sentinel, routing storage through the sentinel key either way),
`internal/executor/sequence_acl_test.go` + `internal/postmaster/
grant_ddl_test.go` (new tests), `.ralph/deferral_ledger.md` (2 new rows, 6th/
7th M0134-0069 entries), `.ralph/fix_plan.md` (M0134-0069 entry updated).

Key symbols: `Catalog.IsOwnerACLRevoked`/`HasOwnerPrivilege` (catalog.go),
`dmlPrivilegePermittedAs` (operators_storage.go:2222), `tryRecordTableGrant`/
`tryRecordTableRevoke`'s new `aclKey` local (grant_ddl.go).

Hypothesis/Findings: the fix is real and verified correct live (autocommit
mode, port 5534) but doesn't move `sequence.sql`'s diff because that fixture
wraps its whole owner-ACL sub-block (lines 294-396, `regress_seq_user`) in an
explicit `BEGIN...ROLLBACK`. Two NEW blockers discovered and ledgered (not yet
fixed): (1) GRANT/REVOKE only gets recorded into the catalog ACL store in
autocommit mode — `internal/postmaster/query.go` (~line 224/242) gates
`tryRecordTableGrant`/`tryRecordTableRevoke` on
`connTx == nil || !connTx.InExplicit()`; inside a transaction it falls through
to `execCompatNoop` (`internal/executor/operators_ddl.go:20839`) which only
does xmax/lock bookkeeping, never the catalog ACL mutation. (2) even once (1)
is fixed, `GrantTablePrivilegeAs` (catalog.go ~16262-16273) unconditionally
clears `relACLEmptied`/`relACLOwnerRevoked` on ANY owner-targeted GRANT
(not just a full re-grant), so a single selective re-GRANT after a REVOKE ALL
restores the owner's full bypass instead of just the one privilege — opposite
of PG's per-privilege-independent semantics. Neither fix is achievable within
a narrow brief; each needs its own sizing/design pass (transactional-DDL
commit path for (1); reconciling the flags' original display-only purpose vs.
this new enforcement consumer for (2)).

Next step: decide between (a) sizing/briefing blocker (1) (transactional ACL
recording — likely the bigger lever, unblocks (2) as well since the fixture
needs both), or (b) picking a smaller standalone M0134-0069 item instead
(Bucket 6 small text/HINT/DETAIL gaps — 9 diff lines, `pg_sequence_parameters`
SRF — 7 lines, `\d` UNLOGGED label + `pg_get_sequence_data` cache mismatch — 2
lines each; full bucketing from this loop's researcher round, agent
`ada9081fbcdb3f63d`, still valid) to keep landing quick wins while blocker (1)
awaits its own design pass. Recommend (b) next loop (Bucket 6, cheapest/
lowest-risk remaining item) unless the banner has moved on from M0134.

Gates run this loop: `go build ./...` PASS; `go test
./internal/executor/... ./internal/catalog/... ./internal/postmaster/...`
PASS (incl. new tests); `scripts/pg-regress-runner.sh --verbose sequence` —
275 lines, unchanged (no regression, confirmed via tester agent);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS (tester
agent, ~full run); `make ralph-state-guard` — found 2 stale markers,
auto-repaired, then PASS; pre-commit pgbench smoke PASS (11864 TPS
select-only, 663 TPS simple-update, 330 TPS TPC-B, 0 failed).

Delegation: researcher agent `ada9081fbcdb3f63d` (1 round — bucketed the
275-line diff precisely, confirmed owner-ACL-revocation fix safety); coordinator
did the root-cause dig itself this loop (sentinel-vs-actual-owner bug in
grant_ddl.go, not caught by the researcher round) before briefing; implementer
agent `a148454f78c4a6f4c` (1 round — landed the full brief, correctly widened
scope to the GRANT twin per its own judgment, escalated NEEDS-DECISION when
the regress diff didn't shrink rather than thrashing — right call, the root
cause was outside its 3-file scope); tester agent `a60a863b4ec838261` (1
round — confirmed no regression, ran both required gates).

In-flight: none. Commit `7ff7303c` pushed to `regress-renumbering`. No server
left running.
