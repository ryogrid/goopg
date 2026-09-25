# R85 — Report: deparse PARAM_EXEC from its owning SubPlan source

Design: `SCOPE.md` (reviewed through three scope reviews, two
scope-amendment reviews, and three implementation reviews; final
implementation verdict APPROVE with no blocking findings).

## Change

`EXPLAIN` now renders a correlated `ExecParamRef` as the direct outer
`ColumnRef` recorded by the owning sublink's `ParParam` / `Args`
pair. The execution slot is unchanged; this is display-only.

The mapping is installed only while that exact SubPlan body renders,
so nested SubPlans shadow and restore their immediate owner's map.
Owner validation is atomic: malformed lengths, negative or duplicate
slots, nil sources, forwarded `ExecParamRef`s, row-constructor IN,
and every non-column expression retain `$N` for the whole owner.

Qualification is proved inside the active owner's query scope with a
dedicated fail-closed plan-node allowlist. The resolver stops at
isolated projects, CTE bodies, set/recursive/DML boundaries, never
walks expression-hanging SubPlans, declines unknown node kinds, and
does not consult the statement-global `bySrc` table. A nonzero source
ID must match; an erased zero ID must name exactly one candidate.
Candidates are de-duplicated by node and RTID. An RTID-bearing
candidate must have an RTID-keyed registered EXPLAIN name; only a
candidate with no RTID may use its truthful base/alias name. Every
unproved case retains `$N`, never a bare column.

## Review findings closed

The implementation review caught and closed three final correctness
holes before corpus testing: missing RTID-name registration no longer
falls back to a base name, repeated RTIDs no longer create false
ambiguity, and a real zero-source-ID forced-qualification path is now
pinned. The review-suggested recognized-owner/nonmatching-source and
unknown-node cases are pinned as `$N` fallbacks as well. Final review:
APPROVE, no blocking findings.

The final report/handoff review also returned APPROVE with no
blockers. Its sole non-blocking traceability note was applied: the
spotcheck stdout is retained below and the R84 A/B inputs are cited.

## Corpus result

The fresh R84 pre-change census confirmed exactly six `$N` display
sites in four queries:

- TPC-H Q17: two `$0` sites; Q20: `$1` and `$0`.
- TPC-DS Q6: one `$0`; Q41: one `$0`.

The normalized A/B guard found zero extra plan-text movement:

- TPC-H: 20/22 identical; only Q17 and Q20 changed.
- TPC-DS: 97/99 identical; only Q6 and Q41 changed. Q36/Q70/Q86
  retain their known querygen syntax errors; their raw captures differ
  only in the temporary filename PID and are identical after that
  capture-envelope normalization.

Every replacement agrees with a fresh live PG 18.3 capture:

- H Q17: `part.p_partkey` at both sites.
- H Q20: `partsupp.ps_partkey` and `partsupp.ps_suppkey`.
- DS Q6: `i.i_category`.
- DS Q41: `i1.i_manufact`.

Prediction P1 holds: Q41 is now `MATCH []` against both the live
`:65438` / `tpcds025` capture and the checked-in PG corpus. The
checked-in-corpus tally moves from `match=1 shapediff=68
missingnode=27 error=3` to `match=2 shapediff=67 missingnode=27
error=3`. No costs, rows, node shapes, or executor behavior changed.

## Gates (2026-09-12)

- `git diff --check`: PASS.
- Focused R85 pins: PASS (owner kinds; atomic malformed decline;
  map lifetime/shadowing; forced direct and zero-ID qualification;
  missing registration; RTID de-dup; ambiguity, boundary, unknown,
  nonmatching, unmapped and nil fallbacks; nested immediate owner;
  plain EXPLAIN and ANALYZE).
- Clean `go test ./internal/optimizer ./internal/executor`: PASS.
  The review lane independently passed the same suites with
  `-count=1` and an isolated Go cache.
- TPC-H Q12/Q13 spotcheck: PASS (`2` / `34` rows).
- SF0.25 values sweep: `PASS=96` (60 checksum-verified, 36
  row-count-only), `MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0
  SKIP=3`. Plan channel: 97 same, changed only Q6/Q41.
- Private candidate captures proved the running executable inode;
  both private servers were stopped cleanly. Live PG references were
  read-only and left running.

## Evidence

- Pre-change baselines: `/tmp/pp2/r84/tpch-r84.txt`,
  `/tmp/pp2/r84/ds-r84.txt`
- `/tmp/pp2/r85/ds-r85.txt`, `ds-pg-live.txt`,
  `ds-parity-fixture.txt`, `ds-parity-live.txt`
- `/tmp/pp2/r85/tpch-r85.clean.txt`,
  `tpch-pg-live.clean.txt`, `tpch-parity.txt`
- `/tmp/pp2/r85/tpch-spotcheck.txt` (retained PASS stdout)
- `/tmp/pp2/r85/sf025-results/sweep-20260912-090705.txt`,
  `plans-20260912-090705.txt`
- Candidate binary `/tmp/pp2/r85/goopg-r85`; private capture ports
  `:5557` (DS) and `:5554` (H), both stopped after use.

## Remaining debt

Forwarded PARAM_EXEC and non-Var source-expression expansion remain
deliberately unsupported and fail closed to `$N`. They are not present
in the current executable TPC-H / TPC-DS display census. Broader source
deparsing requires its own scope.
