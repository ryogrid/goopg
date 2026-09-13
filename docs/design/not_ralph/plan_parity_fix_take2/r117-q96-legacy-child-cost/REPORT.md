# R117 result: Q96's 7.68 margin first diverges at lower Hash Join rows/width

R117 completed the measurement-only child-display-cost audit required by
`SCOPE.md`.  No production cost, Datum representation, statistics, Gather,
executor, or search behavior was changed.  The temporary observation code was
removed after measurement.

## Controlled measurement

The two R111 common-input forced forms ran against the private Goopg cluster
at `127.0.0.1:5568`, built from `05e8c6ad4`, with
`work_mem=64MB`, `join_collapse_limit=1`, `from_collapse_limit=1`,
`GOOPG_GATHER_PATHS=top`, `GOOPG_PARTIAL_AGG_PATHS=on`, `GOGC=off`, and
`GOMEMLIMIT=12GiB`.  For each form, TEXT and JSON EXPLAIN were captured
OFFx2 and ONx2 for `GOOPG_Q96_LEGACY_CHILD_TRACE=1`.

Each same-mode pair was byte-identical, and every trace-on TEXT/JSON output
was byte-identical to its trace-off counterpart.  The trace had no output
under OFF.  All selected explicit Hash Join occurrences had one-to-one final
mapping.  The final LATERAL Nested Loop is a non-target stamped-cost,
non-legacy observation (`ledger=none`); it is not a mapping falsifier and does
not obscure either of the two explicit Hash Join ledgers.

The retained artifacts are `/tmp/r117-{hdem-first,store-first}-{off,on}-{1,2}`
with `.plan` for TEXT and `.json` for JSON.  Each row below is the one checksum
shared by all four OFF/ON repetitions for that form and format.

| form | TEXT SHA-256 | JSON SHA-256 |
| --- | --- | --- |
| hdem-first | `55e91b90fd50fc71ebf2dbaf110662675cc70c9187e31a0d5d21d3287a23c2bf` | `89dc5951eab53a9a33578b491a392678f7fff5a100716d0fff414e9bfe6f5242` |
| store-first | `e2e425453508b8d331f774199ce8bb0cdd00a9457d0253a36db55f1dae09aecc` | `64129a15c2dc5d8252261267c2714c193511e61bae1c5508d9f2d62037c7fac4` |

Trace-on records are in `/tmp/r117-goopg-q96.log` (tokens `r1`–`r8`;
TEXT `r1`–`r4`, JSON `r5`–`r8`).  The trace-off server log is
`/tmp/r117-goopg-q96-off.log` and contains no `Q96CHILD` record.  Direct
value runs are retained as `/tmp/r117-hdem-first.value` and
`/tmp/r117-store-first.value`: both print `266` and share SHA-256
`52e38d6ae56745bbbf881c140fecf94d7b401cf55777c10627547a22991330ae`.

## Reconciled ledger

| forced order | lower Hash Join rows / width / total | upper Hash Join child total / self | upper Hash Join total |
| --- | --- | --- | --- |
| hdem-first | 688,465 / 476 / 14,155.41 | 21,040.18 / 6,580.57 | 27,620.75 |
| store-first | 688,081 / 1,104 / 14,079.69 | 21,032.50 / 6,580.57 | 27,613.07 |

The upper Hash Join self term is exactly equal.  Its `7.68` total difference
is exactly the inherited child-total difference.  The first differing
*selected/root-lineage legacy Join ledger* is therefore the lower explicit
Hash Join: cardinality differs by 384 rows and its output schema width differs
by 628 bytes.  Its display total differs by 75.72.  The intervening
Project/cardinality propagation changes that into the 7.68 inherited
difference at the upper join; it is not a hidden join-method, build-side,
CROSS-promotion, or final-LATERAL cost term.

The scan children make the distinction especially clear.  Both forms retain
the same `store_sales` legacy scan display cost (719,876 rows, width 428,
total 7,198.76).  The forced orders deliberately have different right scans:
72.00 for `household_demographics` (7,200 rows, width 48) versus 0.12 for
`store` (12 rows, width 676), a 71.88 direct child-total difference.  R117
therefore does not claim equal scan costs.  It establishes only that there is
no like-for-like/shared-scan pricing disparity and supplies no evidence for a
physical Datum-size adjustment.

## Producer inventory and limit of the evidence

The source paths for the two observed fields are now identified, but were not
mutated or newly traced:

- `EstimateRows(*optimizer.Join)` dispatches to `estimateJoin` in
  `internal/optimizer/cardinality.go`; its child estimates and join clauses
  produce the lower join's row estimate.
- `TupleWidth(n.Output())` in `internal/optimizer/plancost.go` produces the
  display width from the constructed join schema.  The order-specific
  projection/schema surviving above the lower join is a candidate source-level
  locus to audit: it can explain differing constructed widths even though the
  base scan widths are stable, but R117 did not trace the writer that proves it.
- `DeriveLegacyDisplayCost` merely consumes those already-constructed rows and
  schema widths while recursively attributing child totals.  It is not the
  producer of the divergent estimates.

Thus R117 proves the first differing selected/root-lineage legacy Join ledger,
not the globally first source-level difference: the forced orders already have
different right relation/schema inputs.  A successor must
separately trace and reconcile `estimateJoin` inputs/selectivity and the
required-column/schema construction for these two lower joins before proposing
any change.  It must not infer a Datum-size cause from this result.

## Disposition

R117 closes the legacy child-total attribution question.  It narrows the Q96
breakdown to lower Hash Join cardinality and output-schema/required-column
construction.  The next task is a reviewed, measurement-only producer audit;
it must preserve the R111 forced-form controls and retain the same no-production
change boundary.
