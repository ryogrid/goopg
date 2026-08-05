(idle — nothing in flight)

Last loop: **M0127-P5.9-t CLOSED** — commit `bcb630fc`, pushed. No orphaned
servers. The filed task was wrong twice, and that is the whole finding.

**(1) The `reduce_outer_joins` RIGHT→LEFT flip cannot be represented.**
`parser.FromExpr` is a `Base RangeVar` plus a FLAT `[]JoinExpr` whose every right
side is a single range var — a strictly left-deep chain with no node for a nested
join — so the flipped shape of `a ⋈ b ⋈ c RIGHT JOIN d`, which is
`d LEFT JOIN (a ⋈ b ⋈ c)`, has nowhere to live. Flipping inside the planner's own
tree is a *different* change: a `Join`'s schema is the positional concatenation of
its inputs, so swapping arms renumbers every binding offset and reorders
`SELECT *`. Upstream escapes that only because its Vars are varno-addressed and
`*` was expanded at parse analysis.

**(2) The flip was never what the seam needed.** Because the chain is left-deep, a
RIGHT JOIN's multi-relation side is on the LEFT of the pin — exactly where a LEFT
JOIN's is — so `splitOuterSpine` needed no change at all. What differs is
NULLABILITY: the prefix's ORDER is searchable either way (`deconstruct_recurse`
builds a sub-joinlist for an outer join's nullable arm), but the `WHERE` is not
pushable into it (`check_outerjoin_delay`, initsplan.c).

**Landed.** `spineLinkSearchable` admits a MATCHED LEFT/LEFT or RIGHT/RIGHT pair;
new `prefixNullable` scans the WHOLE spine so one RIGHT link anywhere holds the
entire `WHERE` in the residual above it. Prefix `ON` quals unaffected (they
originate below the join). FULL still declined.

Files: `internal/planner/joinsearchseam.go`, `joinsearchspine_test.go`, new
`internal/executor/right_join_spine_rows_test.go`; 09 §3.22 + `docs/design/
README.md`; ledger row P5.9-t + fix_plan.

Gates run: full units 0 FAIL; `tpch-spotcheck` Q12=2 Q13=35 PASS; pgbench smoke
via the hook (470/641 write TPS, 12.7k read-only); DS05 SF0.5 `PASS=95 (57 ck)
MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=4`, `STATUS-DELTA
verdict-changes=none runtime-moves=0`, `PLAN-SHAPE same=99 changed=0` (report
`sweep-20260806-080412.txt`). The corpus has NO RIGHT JOIN, so the sweep is a
no-regression reading; the demonstration is four reverted-guard probes — with
`prefixNullable` disabled, `… RIGHT JOIN rj_c … WHERE rj_a.id IS NULL` returns
all three `rj_c` rows null-extended instead of the one.

NEXT LOOP (banner in `.ralph/fix_plan.md` wins — M0127 is #3 and current):
**-p** (searched-arm hash batch-growth fixture, units only). Larger: 03 §4.4
`SpecialJoinInfo` inference for the outer link buried below an inner one (Q78).
Ledger P5.9-t follow-up: port `reduce_outer_joins`' actual REDUCTIONS — a strict
`WHERE` qual on the nullable side proves no null-extended row survives, so the
join goes INNER and the qual pushes freely; representable as a TYPE downgrade
(no side swap) in a prep pass before `planFromClause`. Fires on LEFT joins too,
so it is the whole corpus and needs its own bar. Ledger P5.9-u follow-up
(unchanged): populate `TimeSubTime`/`TimeSubTimestampTZ` at their producers and
switch `compareDatum` off the `Scale != 0` timetz inference.

Nightly triage: `ci/logs/action-items.md` is still run 20260806-011323; all 18
subjects already filed under M-NIGHTLY. Nothing new.

In-flight: none.
