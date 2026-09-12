# R91 REPORT — PG virtual buckets for unmatched unique probes

R91 implements the reviewed scope in production commit `63c79406b`. It ports
the non-parallel, skew-enabled PG18 `ExecChooseHashTableSize` geometry into a
private optimizer helper and uses its `numbuckets * numbatches` only for the
unmatched half of an already-proved INNER unique Hash Join. It does not change
Goopg executor map sizing, batches, spilling, or I/O cost.

## Implementation and proof

`pgHashGeometry` models PG's packed `HashJoinTuple` accounting: tuple header,
`MAXALIGN` width, 2% skew reservation, bucket-pointer cap/floor, batching, and
the PG18 walk-back. It rejects unknown output width, invalid memory, NaN/Inf,
and arithmetic states outside the represented model. The denominator is then
`inner_rows / virtual_buckets`, clamped as PG's row estimator requires. The
existing `cpuOperatorCost * numHashClauses` remains the deliberately narrow
hash-qual proxy; no general QualCost, MCV-frequency, or pathtarget costing was
added.

`Path.OutputWidth` is a separate planner-only field. The index-only producer
builds it from exactly its `IndexOnlyCovered` schema through `TupleWidth`; all
other paths fall back to `Rel.Width`. Serial and partial Hash Join candidates
both receive their build path's output width. This representation is isolated
from `NCols`/`AvgVarBytes` and `executor/hashsize`.

The implementation review approved the final code. Focused tests include
discriminating `MAXALIGN` 48/49 and skew-reservation PG vectors, multi-batch
and walk-back vectors, hostile arithmetic declines, actual serial and partial
index-only producer propagation, non-INNER final-cost identity, and an
executor-spill invariant with distinct valid virtual geometries. The required
local suites, vet, and whitespace check pass.

## Runtime evidence

The binary built from `63c79406b` is
`/tmp/pp2/r91/goopg-63c79406b`, SHA-256
`9a7c0789fb0cc72d84e9c0bfa336276a4f9a1ce913f37c50e0d6616d5a75d537`.
All artefacts live under `/tmp/pp2/r91/`.

* TPC-H digest: **24/24 MATCH**, verdict PASS, against
  `bench/tpch/baseline-digests.txt`.
* TPC-DS SF0.25 foreground sweep: **PASS=96, MISMATCH=0, CKMISMATCH=0,
  ERROR=0, TIMEOUT=0, SKIP=3**.
* Fresh PG18.3 census: **match=2, shapediff=67, unparsed=0, missingnode=27,
  error=3, timeout=0**, unchanged from R90's total.

Q96 remains value-correct (`266`) and natural A/A EXPLAIN is byte-identical,
SHA-256 `1abd71777dd802abf81a5704f85536a2be1a6a3b8579136568b7e9d58dfde828`.
The serial prefix now follows PG's relation order:
`store_sales -> household_demographics -> store`; its top cost is `24630.79`.
It is not yet structurally equivalent because PG still selects the parallel
partial-aggregate/Gather plan. `GOOPG_GATHER_PATHS=top` trace-off and
`GOOPG_PGSHAPED_DP_TRACE=1` trace-on are byte-identical for Q9, Q41, Q91, and
Q96. Their hashes (Q9/Q41/Q91/Q96) are respectively
`6b95aee0b380e7ca48d788b951d4ca82eb515f0c79e1af828c91251f36c384c1`,
`9623554c37569f95a31249a01494577935f03ea870524fb2516328cb898cb35a`,
`6ca2f1686a07a9e9ee675c1dd03b48a548131bbd261aba6ef8c928a18c1a112a`, and
`1abd71777dd802abf81a5704f85536a2be1a6a3b8579136568b7e9d58dfde828`.

## Next boundary

R91 closes the represented unmatched virtual-bucket gap. It improves Q96's
serial join order but does not authorize forcing parallelism or importing the
remaining PG final-cost inputs. The next iteration must identify a separately
scoped PG-compatible reason for the remaining partial aggregate/Gather choice,
or another independently evidenced high-leverage parity mismatch.
