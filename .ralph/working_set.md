(idle — nothing in flight)

## Loop summary (2026-07-12, loop #61)

**Nightly triage:** action-items batch `20260711-011536` (same as #58-#60) —
all 3 AI items already `[x]` in M-NIGHTLY (co-load timing flakes +
PgWaldumpVacuumPruneRoundtrip). No new nightly work.

**Task — M0122-0008 / unimplemented_feat.json SASLprep+channel-binding+
scram_iterations.** Verify-before-implement: 2 of 3 sub-features already done
(SASLprep fully ported+wired at both scramBuildSecret sites; scram_iterations
GUC read live on the password-set path role_ddl.go:320). Channel binding PROPER
is architecturally blocked — goopg has no TLS (server.go:1211 rejects
SSLRequest). Closed the one implementable half: no-binding downgrade protection.

Landed:
- internal/auth/scram.go: new SCRAMServer.cbindFlag field; handleClientFirst
  records the gs2-cbind-flag; validNoBindingChannelAttr(c, flag) now ties c= to
  the original flag (n→"n,,"/biws, y→"y,,"/eSws) per auth-scram.c
  read_client_final_message:211 (was accepting either → downgrade-tamper gap).
- internal/auth/scram_test.go: TestSCRAMExchangeRejectsChannelBindingFlagMismatch
  (non-vacuous: asserts NOT ErrInvalidPassword, i.e. caught at c= before proof),
  TestSCRAMExchangeAcceptsYFlag.
- unimplemented_feat.json: added code_audit narrowing (kept open — channel
  binding blocked on TLS). Surgical Edit, JSON re-validated.
- docs/design/0049-0003-scram-sha-256.md §3b + 2 test rows; README row updated.
- .ralph/deferral_ledger.md: `-` row (channel binding proper deferred, TLS blocker).

Gates: go build ./... clean; go vet ./internal/auth clean; ./internal/auth PASS;
server+initdb SCRAM/Auth/Role tests PASS. (Auth/protocol change — not
executor/planner, so TPC-H spotcheck N/A; pgbench smoke via pre-commit hook.)

Next loop: unimplemented_feat.json ~97 open. Channel binding proper needs TLS
first (large). Other bounded candidates: WAL segment recycling, per-CTE
pg_stat_* tracking. Avoid: pg_index expression-index restart persistence (hard
null-bitmap decode), parallel-query GUC stubs (moot — no parallel executor),
date/interval hot area.

In-flight: none
