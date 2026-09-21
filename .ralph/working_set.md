(idle — nothing in flight)

# Loop #83 — M0145-0004: `tlist_same_datatypes` landed (task stays `[ ]`)

Banner: 0018 is owner-blocked, so the flow chain resumes at **M0145-0004**
(still `[ ]` — this loop landed ONE of its residuals, not the task).
Design: `docs/design/0100-0149/m0145-0004-union-all-appendrel-leaf.md`,
final section. Movement: none (default arm identical).

## The change
`SetOp.TlistTypesDiffer` captures upstream's `tlist_same_datatypes`
(`tlist.c:257`, driven from `prepjointree.c:2258`) inside the fold,
**immediately before `setOpUnifyBranches` coerces the branches** — the only
moment it is answerable: the parser AST has no types at mark time, and after
coercion every branch agrees by construction. `addAppendRelPartialPaths`
consults it and refuses the hoist. Zero value = "no known difference", so the
partition/inheritance fan-outs are untouched.

## ⚠ THE TRAP THIS LOOP HIT — read before touching type comparisons
The first version compared `Type.Name` directly. **Upstream compares OIDs**,
where `decimal` IS `numeric` (1700) and `int` is `int4`. TPC-DS Q5's union
mixes a table column with `cast(0 as decimal(7,2))`, so it refused a union PG
flattens and **removed the Parallel Append shape M0145-0004 itself landed**.
Fold both spellings through `catalog.ArgTypeDisplayAlias` first.
Knob-arm capture named it exactly: naive → Q5 moved; alias-folded → only
Q36/Q70/Q86, which are the capture's three pre-existing parse ERRORS whose
text embeds the temp FILENAME (the loop #82 trap, hit again — do not chase).

## Tests — three levels, each non-vacuity checked
predicate (alias + typmod pins) / gate (matched-types control is load-bearing)
/ **WIRING** — the last was added because neutralising the capture site left
the other two green: both ends pinned, connection not.

## Gates — ALL PASS
units; tpch-spotcheck Q12=2/Q13=33; tpcds-sf025 `PASS=96 MISMATCH=0
CKMISMATCH=0 ERROR=0 TIMEOUT=0`, plan channel `same=99 changed=0`; acceptance
arm 24 MATCH; both default-arm floor captures held EXACTLY (TPC-DS `match=2`
Q9+Q41, TPC-H `match=1` Q6 — M0144-0001's re-pin, not AGENT.md's `>=3`);
pgbench smoke; state guard.

## Ledgered residual
The comparison is by DISPLAY NAME, not type OID — domains over one base type
would read as the same. Errs toward ALLOWING (never refuses what PG allows).
Resume: `sameSetOpTypeName` once a Schema column can answer its own pg_type
OID; catalog has no exported Type→OID primitive today.

## Next loop
M0145-0004's remaining residuals, in its "Still open" list: member-level
rtable + qual distribution (deferred to **M0145-0005**), LATERAL union
propagation, CTE-wrapped union leaves (Q2/Q14/Q71/Q76), serial-side member
path competition. Or move to **M0145-0005** per the banner chain.

## Owner escalations — four open
partition_aggregate inventory row; template1 collision (A vs B); M0145-0018
(option re-take AND the criterion-1 waive, loop #82 evidence); M0145-0012
re-sequencing behind M0145-0020a.
