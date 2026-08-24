Task just completed: M0134-0155 (psql.sql) — sized live + scoped fixes
landed, committed f2d54e226 on regress-renumbering (loop #46 had been
killed mid-gate by the operator; this session finished and landed its WIP).

What landed: (1) SET ROLE TO/= generic_set spellings end-to-end — parser
role arm (= is TokenOperator, NOT acceptSymbol; first cut was silently
broken until new parser subtests caught it) + query.go/extended.go fast
paths via stripSetToOrEquals. (2) SESSION AUTHORIZATION deliberately
rejects TO/= (gram.y:1764/:1774 only; oracle-verified): setAuthzGenericSetForm
routes those from both protocols' fast paths to the parser for the true 42601.
(3) NEW shared-core rule surfaced by oracle-diff: PARSE-time errors inside an
explicit txn now abort the block (connTx.Fail at dispatch.go parse-error
return + dispatch_extended.go qerr return; mirrors M0132-S5); empty
statements stay allowed in aborted blocks (#17983 parity,
allowedInAbortedBlock). (4) CREATE TABLE (...) USING <am> accepted-and-
discarded, both parseCreateTableTail arms. Harness: pg-oracle-diff.sh
--auto-start fixed (initdb -q removed — PG 18.3 has no -q; -U "$USER_"
pinned). Tests: TestStripSetToOrEquals, TestFastPathGenericSetSpellings,
TestParseErrorAbortsTransactionBlock, TestParseCreateTableUsingAccessMethod,
parser SET subtests.

Gates run: units PASS (43 pkgs), full postmaster pkg PASS under cap,
parser PASS, regen-testport + check-testport-inventory PASS, commit-hook
pgbench smoke PASS, 16-statement oracle-diff vs PG 18.3 matches except ONE
character (error text echoes lowercased 'to' vs scan.c raw-source 'TO').

Residual psql.sql gaps ledgered (.ralph/deferral_ledger.md M0134-0155):
63 goopg-side ERRORs over >=6 subsystems — role-does-not-exist x14,
syntax-error x11 (incl. "unsupported statement (got X)" template divergence),
spurious NOT NULL x6, permission-denied-create-role x5, function-missing x5,
table-permission denials x9 (M0134-0154 ACL core), pg_roles.rolinherit x2.
Highest-leverage follow-up remains the ACL-check core (PUBLIC fallback +
RoleIsMemberOf walk) per M0134-0154's recommendation.

NEXT LOOP: per Current Priority banner + task-ID-ascending, work
**M0134-0156 (psql_crosstab.sql)** next.

Peer-session WIP still present, leave uncommitted: .ralphrc, progress.json,
analysis/postgres-oracle-compatibility-report.md, ci/logs/launch.log,
docs/wiki/getting-started.md, docs/wiki/modules/catalog.md,
internal/executor/operators_recursive_cte.go (nil-guard for o.ctx.Ctx),
postgres symlink, third-party/tpcds-postgres submodule,
analysis/deferral-ledger-summary-20260824/, dl_summary_session.txt.

In-flight: none.
