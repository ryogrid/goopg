# R96 P1: slice menu from the attributed term

## Term-by-term audit (done in scoping, code-reading only)

PG `initial/final_cost_hashjoin` (`costsize.c:4160/4275`) vs goopg
`hashJoinCost` (`cost_funcs.go:640`) for plain INNER joins:

| term | PG | goopg | verdict |
|---|---|---|---|
| outer run / inner build / probe-hash | identical formulas | identical | match |
| bucket walk | `hash_qual.per_tuple × outer × clamp(inner×bs) × 0.5`, bs defaults **1.0**, refined by `estimate_hash_bucket_stats` (never skipped) | same formula **iff `innerBucketSize > 0`, else SKIPPED** | **DIVERGER (a)** |
| qp/output/tlist | `cpu_per_tuple × hashjointuples` + pathtarget per-row | `cpuTuple × outputRows`, no pathtarget | cancels (both orders output 5477); not a decider |
| spill batching | `numbatches > 1` page charges | same (`hashsize.Choose`) | match (NBatch=1 here) |

Why (a) decides Q96: the walk scales with outer×inner. Store-first
walks 57322×720 buckets; hdem-first walks 68777×1. If goopg skips
(bs=0) while PG charges (bs≥refined value), PG penalizes store-first
by up to thousands while goopg sees a 157.5 margin the other way.
The lead hypothesis is therefore precise and falsifiable: read
`innerBucketSize` for both joins.

## Slices

**(a) Bucket-walk transcription audit (LEAD).** Step 1
(measurement, temp-or-unit): report `innerBucketSize` + walk term
for the {0,1} and {0,3} joins — skipped (0) or valued? Step 2,
iff mis-transcribed vs PG (wrongful skip with usable stats, or
wrong fallback where PG uses 1.0): minimal fix with PG-term-keyed
comment. Blast radius: GLOBAL hash-join costs — full gates
(values/sweep/pp both corpora) + join-order-sensitive controls
(TPC-H Q5/Q7/Q8/Q9 + TPC-DS Q96 family) mandatory. Success bar:
Q96 flips to hdem-first with values green, no unplanned flips.

**(b) Fuzz tiebreak at final election (CONDITIONAL).** Valid only if
(a) measures valued-and-agreeing yet PG still prefers hdem-first —
i.e. PG's two prices genuinely tie. Needs PG's loser price, which
EXPLAIN cannot show; options: hand-compute PG's formula from
`pg_stats`, or a sensitivity probe (vary work_mem/stats targets,
watch PG flip). Do not implement blind tiebreak-weakening: it would
move every close election in the corpus.

**(c) Ruled out (do not scope):** rows/sizing (0.1%), widths
(row-dominated terms; R70 stays closed), pathtarget/tlist (cancels),
spill (NBatch=1 both).

## Bounds (inherited from SCOPE)

No join-order forcing, no election-rule rewrites beyond (b)'s
conditional, no width model changes, no NLI/Memoize work. Q9/Q41/Q91
read-only. Implementation is R97+ under a slice SCOPE citing this
menu; step-1 measurement may proceed under R96's P0 bar
(temp, foreground, byte-identity) without a new scope.
