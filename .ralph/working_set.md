(idle — nothing in flight)

Last loop: **M0127-P5.9-g CLOSED** (Q2's flag-ON 0-rows). Do NOT re-derive:

1. **It was NOT the splice.** At 4-under-5 the P5.9-f `outerWidth` fix
   generalises fine — `LeftKey`/`RightKey`/`Predicate`/residual are all
   correct. The defect is one level down, INSIDE the decorrelated
   `HashAggregate`: **group key and aggregate argument were in different
   coordinate scopes**. `SubCol` is recorded where the correlation is
   *collected*; `harvestIndexKeyParams` records a LEAF-relative `is.Output()`
   index and never accumulates an offset. `ps_partkey/0` agreed by accident
   (partsupp is the sub's first relation) until P5.9-c's rotated map moved it
   to 14 — then `/0` read `r_regionkey`, one group keyed 3, 0 rows.
2. Fix: `resolveSubColInSchema` (unnest.go) — identity → name+`SourceTableIdx`
   → **nil BAIL to the SubPlan**. Applied at `buildUnnestedSubquery`'s GROUP BY
   and the sibling `unnestScalarWithResiduals`' two `leftWidth + SubCol.Index`
   sites. Producer still has no scope contract (ledger row).
3. **The reproducer needs the TPC-H PKs.** Without them the correlation stays
   in a Filter, both spaces coincide, both arms return 18 — a fixture that
   cannot fail. Full recipe: `analysis/leftdeep-joins/p59g/README.md`.
4. Incidental: **`DROP INDEX` errors "does not exist" on a restart-surviving
   index** while `pg_indexes` still lists it and the planner still uses it.
   Cost one wasted bisect. Ledgered.

Files: `internal/planner/unnest.go` (resolver + 2 call sites + caller bail),
`internal/planner/joinsearchunnestgroupkey_test.go` (new).
Docs: 09 §5.22, bundle README status, IMPLEMENTATION-TODO P5.9-g,
fix_plan P5.9-g + the P5.9 run-3 note, 3 ledger rows,
`analysis/leftdeep-joins/p59g/` (arms + README).

Gates run: **UNITS** (green), **SPOT** (Q12=2, Q13=35, 28.3 s), **DS05**
(PASS=95, MISMATCH=0, CKMISMATCH=0, ERROR=0, TIMEOUT=0, plans 99/99 same,
no verdict changes), **Q2 SF1 arms on ONE binary `c8fe0d352d75b67e` →
`tpch-runner -diff` `Q2 MATCH rows=455` VERDICT: PASS**, PG-18.3 adjudication
on the fixture, pgbench smoke via the commit hook, `make ralph-state-guard`.
Negative control on the new test verified (reverts to `ps_partkey/0` reads
`r_regionkey`).

Nightly triage 20260805-014309: unchanged run, both items already filed under
M-NIGHTLY, left unchecked per the banner.

Next step: **M0127-P5.9 run 3** — clause 1 has no known flag-owned failure
left. One binary, both arms via `NO_BUILD=1 PGSHAPED=0|1
scripts/tpch-acceptance-arm.sh`, `-diff`, THEN the §4 ratchet baseline, §5
audit and DS05 clause in that order. Expect clause 2/3 to still fail (that is
P5.9-h); run 3's value is the first §4/§5 measurement on a correct build.

In-flight: none.
