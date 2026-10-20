# M0146-0241 — an OR join clause's arm pairs its range bounds

Status: landed 2026-10-20 — CLOSED (TPC-DS SF1 97 → 98/99: Q13 matches PG).
Evidence: `analysis/m0146/m0146-0241/`.

## 1. Record

The S9 pass at 4e0c4e8c1 found one join-method record (SF1 Q13) and one
qual-placement record (SF1 Q72). Q13 was chosen: lower id, shallower
divergence.

PG joins customer_address before store (Nested Loop → Index Scan
customer_address_pkey, then Index Only Scan store_pkey). goopg hash-joined
store first. The customer_address join's row estimate was the lever: PG takes
34 rows to 3, goopg took 33 to 7.

## 2. Diagnosis (SF1 clone vs :65438)

| expression over store_sales ⋈ customer_address | goopg | PG |
|---|---|---|
| no qual | 2749683 | 2749683 |
| one arm `ca_state IN (…) AND ss_net_profit BETWEEN …` | 11520 | 11520 |
| OR of two such arms | 31665 | 16054 |
| OR of three such arms (Q13's) | 80704 | 28320 |
| `ca_state IN (…) OR ca_state IN (…)` | 494283 | 494283 |

The OR of ranged AND arms, as a restriction on one table, agreed too. The
divergence was confined to an OR that is a join clause, because its arms read
two relations.

## 3. PG mechanism

clause_selectivity_ext prices an OR through clauselist_selectivity_or. Each
arm is an AND, priced through clauselist_selectivity_ext. That function pairs
the bounds of one variable into a range (addRangeClause, clausesel.c): the
selectivity becomes `hibound + lobound - 1`, plus the null fraction, with
DEFAULT_RANGE_INEQ_SEL when a bound is the DEFAULT_INEQ_SEL guess.

The pairing needs only a single-relation clause, `var <op> pseudoconstant`.
That holds inside a join clause's arm too: `ss_net_profit >= 100` reads
store_sales alone.

## 4. Change

joinselectivity.go's orArmSelectivity multiplied every conjunct. It now
groups single-side inequality bounds by variable, the column's
SourceTableIdx plus name (orArmRangeBound). Each bound keeps
orRangeSelectivity's estimate, and the more restrictive of two similar
bounds wins. orArmRangeSelectivity then completes each variable as
clausesel.c does:

- a lone bound is its own selectivity;
- a pair is `hi + lo - 1` plus the column's null fraction;
- a pair containing a DEFAULT_INEQ_SEL bound gives DEFAULT_RANGE_INEQ_SEL;
- the result is clamped: below −0.01 it becomes DEFAULT_RANGE_INEQ_SEL, and
  at or below 0 it becomes 1e-10.

conjunctionSelectivity (rangequery.go) is the restriction path's twin, and
the two follow the same rules.

## 5. Measurements

- All five rows of §2 now equal PG's.
- **SF1 Q13:** MATCH, cost 82716.24..82716.26 against PG's 82716.25..82716.26.
  Fire-set SF1 97 → 98; only Q13 fired.
- **SF0.25:** 99/99. Q13's plan changed and still matches.
- **TPC-H:** 22/22. Q19's OR join (the brand/container/quantity arms) now
  estimates PG's 47 rows, up from 71.

## 6. Pin

`internal/postmaster/or_join_clause_range_bounds_test.go`
(TestORJoinClauseArmPairsRangeBounds) checks two OR join clauses against
PG 18.3's row estimates, 743 and 1940. It fails without the pairing.

The file name avoids an `_arm_test.go` suffix: Go reads `_arm` as a GOARCH
build constraint, and the test would never run on amd64.

## 7. Not done (ledgered)

- PG runs extended statistics (statext_clauselist_selectivity) over each OR
  arm's clause list before pairing; goopg's OR arms do not consult them.
