(idle — nothing in flight)

Loop #38 landed and committed clean: M0119-0004 DU-002 slice 412
(`regoperator`/`regprocedure` schema-qualification, closing the loop #37
deferral). Verified against a live PG 18.3 instance; all gates green
(build/vet, catalog+executor+parser+server+planner suites, pg_dump port
test, TPC-H spotcheck Q12=2/Q13=33, pgbench smoke pre-commit). Pushed to
origin/align-data-structure-with-pg.

Next candidates (backlog, per the M0119-0004 ledger's open rows):
(1) Builtin operator catalog (pg_operator rows for builtins, keyed by
name+left/right type) — large, standalone feature; now the SINGLE LARGEST
remaining blocker for a realistic (non-custom-operator) `op_family`/
`op_class` pg_dump fixture, since upstream's own fixtures reference real
builtin cross-type btree operators, not user-defined ones. (2) FOR ORDER BY
sort-family resolution (small) — `parseCreateOpClassTail`'s FOR ORDER BY
branch is parsed-and-discarded; needs `OpClassMember.SortFamily` +
`AmOpMember.SortFamilyOID`/amoppurpose='o'. (3) M0119-0005/0006/0007
(pg_waldump/pg_amcheck/pg_basebackup server tiers). (4) M0119-0002 (CLOG
store swap Part B) — flagged highest blast radius, needs dedicated
full-gate session. (5) datacl (pg_database ACL) — permanently deferred.

Recommendation for next loop: (1) the builtin-operator catalog is the
highest-leverage next step for M0119-0004 (unblocks BOTH regoper/regoperator
resolution for builtins AND opclass-member OPERATOR/FUNCTION resolution for
realistic fixtures) but is large — decompose into its own multi-loop design
doc before coding (curate pg_operator.dat rows via the existing
cmd/gen-pg-proc-data-style generator pattern, keyed by name+lefttype+
righttype, feeding a new pg_operator VirtualRows branch + regoper/
regoperator CastExpr builtin lookup). (2) is a quick, well-isolated
alternative if a smaller loop is preferred.
