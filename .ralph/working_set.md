(idle — nothing in flight)

Loop #54 completed and committed/pushed: domain NOT NULL/CHECK constraint
enforcement on INSERT (M0122-0005, closed deferral ledger row 542 from
2026-07-06). New `checkDomainConstraintsForRow`/`evalDomainCheckExpr`
(internal/executor/operators_fk.go), wired into insertOp.Next() (both
non-partitioned and partitioned-leaf paths, operators_storage.go). CHECK now
covers generic predicates (`CHECK (VALUE > 0)`) via a mini-query evaluator,
not just the `VALUE IN (...)` fast path. Also fixed a latent
StringValue()-vs-Format() bug (int-domain IN-checks always rejected every
value). Verified against a real running server (all 3 ledger repro cases:
nn_int/pos_int/small_num) + new unit tests + tpch-spotcheck (Q12=2/Q13=33).

Next candidate (pick ONE): the freshly recorded "UPDATE enforces no
table-level NOT NULL/CHECK constraints at all" gap (deferral ledger
2026-07-06 tail row — bigger than domains, affects every table; resume point:
mirror insertOp.Next's NOT NULL + checkConstraints + checkDomainConstraintsForRow
sequence into updateOp.Next/updateWithFrom in operators_storage.go, then
operators_upsert.go's applyInsert/applyUpdate for the ON CONFLICT DO UPDATE
path), or resume the M0110-0001 multi-database isolation survey (see
fix_plan.md "Current Priority" banner), or survey .ralph/deferral_ledger.md
for another fresh open (`status = -`) row.
