(idle — nothing in flight)

M0127-P5.6-c is DONE and committed: the clamp discipline in
`internal/planner/joinrelsize.go` (`keyImpliedRowsBound`, `superkeyEstimate`,
the two clamps in `calcJoinrelSize`) plus PG's `*isdefault` carried out to the
caller in `internal/planner/joinselectivity.go` (`eqJoinSelectivityExt`,
`joinClauseSelectivityExt`; the old names are thin wrappers). Still inert.

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this note).
It parks M-NIGHTLY below M0127, so the banner selects `M0127-P5.6-d` — delete
the quadratic build penalty (bushy.go:632) once 04 §4's honest batch-I/O term
prices what it stood in for. NOTE: P5.6-d's bar is UNITS + DS05, and the
penalty is in the LIVE bushy planner, so unlike -a/-b/-c this one CAN move
plans — the DS05 sweep (~1 h) is mandatory. Consider doing P5.6-e (the
estimate audit, UNITS + DS05 + audit run) first if -d's batch-I/O term
(P5.7) is judged not yet in place. IMPLEMENTATION-TODO P5.6-d/-e.**

Carry-over facts a next loop should not re-derive:

- `superkeyJoinSelectivity` now returns `superkeyEstimate{sel, residual,
  fired, rowsBound}`; `rowsBound` is +Inf when nothing is provable.
- The key-implied bound is taken ONLY when the key rel is the whole of its
  side (`outer.Relids == RelSet(1)<<keyRel`); a multi-rel side may have
  duplicated the key rel's rows. Ledgered.
- With consistent inputs the superkey product lands exactly ON the bound —
  the clamp only bites when `RelOptInfo.Rows` on the key side has outgrown
  `relInfos[i].baseRows` (stale ANALYZE). That is what the tripwire test
  `TestCalcJoinrelSizeKeyBoundClampsStaleStats` constructs.
- `max(l,r)` cap fires only on `!fired && allDefault && len(residual)>0`;
  `allDefault` uses the isdefault of the side that WON the denominator.
- Divisor rule (unchanged): UNIQUE index ⇒ its OWN relation's raw count;
  declared FK ⇒ the PARENT's. `provenKey.keyRel` follows the same asymmetry.
- Legacy `uniqueNoFanoutRawCount` has it backwards — ledgered, dies with P6.3,
  do NOT "fix" it in the live planner.
- Still open from earlier: P4.1 ledger row #3 (`mergeJoinStream.bufferGroup`
  twin); `pushOneConjunct` not taught the searched tag; `walkPlanExprs` misses
  `Aggregate.Passthrough`/`AggregateCall.Filter`/`WindowFunc`.
- Do NOT `git stash`; gofmt baseline go1.25 (never wholesale `-w`); `cd`
  persists across Bash calls — use absolute paths.
- Gate recipes — SPOT: `scripts/tpch-spotcheck.sh` (~30 s + build). DS05:
  `scripts/tpcds-sf05-regression.sh sweep` (~1 h). PLAN:
  `bench/tpch/setup_goopg.sh` → `PATH=$PWD/postgres/local_install/bin:$PATH
  make plan-gate` → `bench/tpch/stop_goopg.sh`.

Gates run this loop: build+vet clean; gofmt clean on the 4 touched files;
7 new tests PASS; full `internal/planner` PASS; UNITS PASS (exit 0, 0 FAILs,
`/tmp/units_p56c.log`); pgbench SMOKE via the commit hook. DS05 + SPOT + PLAN
not applicable — the sizer still has no production caller.

Nightly triage: still the same 17 `AI-20260804-005028-*` subjects from run
20260804-005028, all already filed under M-NIGHTLY. Nothing new to file.

In-flight: none.
