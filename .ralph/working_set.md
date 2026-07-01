(idle — nothing in flight)

Loop #62 landed + committed + pushed (8b8678fc): `dispatch_extended.go` vs
`dispatch.go` type-formatting-switch divergence closed (M0119-0004, design
`0119-0004-extended-protocol-type-format-parity.md`) — resolves the loop #61
ledger row's resume point (2). Extracted the per-column-type wire-formatting
switch (float4/float8/char/bpchar/date/time/timetz/bytea/regclass) out of
`dispatchSimpleQueryViaExecutor`'s inline loop into a shared
`(*Server).appendTypedCellText` method (`internal/server/dispatch.go`);
`executeExtendedQueryViaExecutor` (`dispatch_extended.go`, previously
float4/float8-only) now calls the same method, so both wire protocols agree
on date/time/timetz/bytea/regclass rendering, not just floats. New
`internal/server/dispatch_extended_types_test.go` →
`TestExtendedQueryTypedColumnsMatchSimpleQuery` (raw Parse/Bind/Execute/Sync,
forcing the extended path with zero bind params) confirmed FAILING pre-fix
(date/time rendered as the generic KindTime full-timestamp fallback) and
PASSING post-fix. Gates: build/vet clean; full internal/server suite PASS;
internal/executor+internal/catalog+internal/planner suites PASS; TPC-H
spotcheck Q12=2/Q13=33 PASS; pgbench smoke = pre-commit hook PASS (ran live
on commit). Design doc + README index added (0119-0004bi). Ledger row
appended (resolved). fix_plan.md intentionally NOT edited (driver-churn —
see memory: record progress in ledger + working_set only).

Next candidates: (1) the bigger regproc-generic-output gap (no OID→name
resolution for regproc-typed columns at query-output time in EITHER protocol
now — pg_type.typinput/typoutput, pg_operator.oprcode/oprrest/oprjoin,
pg_am.amproc all still render raw OIDs; needs a general index promoted out
of internal/initdb to a leaf package internal/executor + internal/server can
both import, mirroring the leaf-config-package precedent for version
constants); (2) M0119-0005 (pg_waldump server tier), M0119-0006 (pg_amcheck
server tier), M0119-0007 (pg_basebackup recvlogical, blocked on logical
decoding) — see fix_plan.md; (3) M0119-0002 (CLOG store swap Part B) also
still open.
