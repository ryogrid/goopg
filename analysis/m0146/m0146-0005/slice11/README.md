# M0146-0005 slice 11 (M0146-0005k): the search's all-default max(l,r) cap retires

`calcJoinrelSize` no longer clips a join whose clauses were all
default-guessed to max(outer, inner) rows (M0126-0010). PG's
`calc_joinrel_size_estimate` has no such clamp. The recon (M0146-0005j,
`../recon-0005j/`) showed the ledgered MCV precondition cannot bind, and an
A/B moved nothing the wrong way.

The plan-node estimator's own cap (`estimateJoin` fallback,
cardinality.go) stays. That fallback also serves nested-loop joins whose
columns DO have statistics, because it measures only hash/merge keys, so
its cap compensates for its own missing measurement.
`TestFallbackCapFiresForNonHashAlgoDespiteStats` pins that behaviour.
Ledgered.

Results:
- `sf025-sweep.txt`: 96/96, 4 plans changed (Q2, Q8, Q44, Q64). Q11's
  2.0x runtime move has an unchanged plan, and Q78 moved the other way
  (noise).
- `tpcds-fireset.txt`: no timeouts. `census-diff-sf1.txt`: Q44's depth-1
  record changes category, and nothing gets shallower.
- `tpch-plan-parity.txt`: 5/22. Spotcheck PASS; the acceptance arm has 24
  MATCH.
- Q44's rank join now estimates about 151k rows (PG 147099). Its next gap
  is join order (`item` joined below the rank merge join).
