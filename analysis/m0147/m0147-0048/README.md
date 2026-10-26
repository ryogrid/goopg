# M0147-0048 — OFF-arm measurement (GOOPG_EXISTS_TO_ANY=off)

Captured 2026-10-27 at `b2665b45c` with `GOOPG_EXISTS_TO_ANY=off` exported to the capture servers, through
`scripts/jointree-parity-capture.sh`. The TPC-DS `.plans.txt` flag stamps read `GOOPG_EXISTS_TO_ANY=off`. The TPC-H
lane printed no stamp, so whether the env reached it is unconfirmed; TPC-H has no OR-of-EXISTS witness either way. The
ON default at the same sha is `analysis/m0146/m0146-0014/routing-20261027h/`.

| corpus | OFF (per-row SubPlan) | ON (unconditional hashed ANY) |
|---|---|---|
| TPC-H | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | 97 / 99 — Q10, Q35 SHAPE-DIFF | 99 / 99 |
| TPC-DS SF1 | 97 / 99 — Q10, Q35 SHAPE-DIFF | 99 / 99 |

Q10 and Q35 are the OR-of-EXISTS witnesses `exists_to_any.go` was written for: PG 18.3's own plans show
`ANY (… = (hashed SubPlan n).col1)`. Their categories under OFF are join-order, parameterisation and qual-placement.

Neither arm is PG's logic. PG plans both the correlated EXISTS and `convert_EXISTS_to_ANY`'s hashed ANY in
`make_subplan` (`postgres/src/backend/optimizer/plan/subselect.c`), and setrefs.c keeps the cheaper for the expected
number of calls (AlternativeSubPlan). goopg's ON always takes the hashed form; OFF always takes the per-row form.
Porting PG's choice is M0146-0012b.
