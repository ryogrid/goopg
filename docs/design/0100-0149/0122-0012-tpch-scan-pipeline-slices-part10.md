# M0122-0012 — scan pipeline slices, part 10

Continues [part 9](0122-0012-tpch-scan-pipeline-slices-part9.md), split at slice 102 under the 800-line design-doc
rule (D3). The task, its return-first rule, Candidate pool and Below-bar list live in the M0122-0012 entry of
`.ralph/fix_plan.md`; the census these slices target is `analysis/m0122/m0122-0012/census-20261027/`.

## Slice 102 — C19: pg_checksum_block over in-place 32-lane rows (Q17 −13.2 % instructions)

Loop #198, 2026-10-27. The G3 profile of TPC-H Q17, G2 #4, puts `pageChecksumBlock` at 12.25 %. That is data
checksum verification on every buffer read; Q17 reads lineitem twice. Both goopg and release PG run with
checksums on.

Q14's index-scan string copies (`slicebytetostring`, ~3 %) were also noted but not taken: an arena would cross
the borrow and lifetime rules, the P0-E12 class.

**Change.** `pageChecksumBlockRows` replaces the per-word byte-assembling loop for whole, aligned pages on
little-endian hosts. The old form remains as the fallback and as the test oracle. Details are in
`0102-0019-initdb-data-checksums.md` §"The rows form of pg_checksum_block".

**Target (written before measuring).** TPC-H SF1 Q17, instructions:u per query (`tools/h-instr.sh`, 5 runs).

| run | before | after |
|---|---|---|
| 1 | 32.104 G | 27.848 G |
| 2 | 32.082 G | 27.862 G |

That is **−13.2 %**, which meets the bar.

Gates: storage `-race` (checkptr on the unsafe view); units; spotcheck; acceptance 24 MATCH; sf025 PASS=99 with
plans 99/99 same; TPC-H census 22/22; RegressSuite = baseline (plpgsql flap); isolation PASS; checksum and
amcheck port tests PASS.
