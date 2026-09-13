# R97 STEP-1 outcome: skip hypothesis FALSIFIED; innerUnique branch active

Ran under R96's P0 bar (TEMP env-gated logs, foreground, EXPLAIN ×2
byte-identical and identical to R95's opt-in plan; TEMP fully reverted,
tree clean). Scratch was rebuilt in `/tmp/r97goopg/` after a peer
cleanup wiped `/tmp/pp2/`; committed history untouched.

## Measurements (relid-tagged per-call logs, Q96 opt-in EXPLAIN)

Bucket sizes are VALUED, not skipped: hdem build bs=0.001389
(≈1/720), store build bs=1.0, time_dim joins bs=0.000523/0.000822,
reversed arms bs=0.169853. SLICE.md's lead mechanism (skip-vs-charge)
is FALSIFIED for Q96 — both engines charge the walk.

Bigger finding: `final.innerUnique=true` on every big-outer hash call
(PROBE.md assumed the plain branch). The walk terms are therefore the
R91 matched/unmatched pair (~13.33 store-first vs ~14.76 hdem-first),
not 72–86. Exact margin decomposition (both filed startups symmetric
at 150.16 = 149.00+1.16 both ways):

- oRun: +127.44 against hdem-first ({0,1} run 22787.58 vs {0,3} run
  22660.14)
- probe (outer cardinality 68777 vs 57322): +28.64
- walk (R91 terms): +1.43
- output rows: 0 (both 5477)

Per-row join increments AGREE with PG: goopg hdem-first
242.63/68777 = 0.00353 vs PG 79.11/22198 = 0.00356. The models agree
per row; the election differs because the LEVEL-2 runs differ — which
themselves contain R91-branch walks over `outerMatchFrac` and the R91
virtual-bucket geometry.

## Reframed slice (a)

Audit the inner-unique INPUTS, not the walk formula (which matches PG
term-by-term): `outerMatchFrac` provenance for the {0,1}/{0,3} joins,
and `pgHashGeometry` virtualBuckets vs PG's `numbuckets × numbatches`.
The fuzz-tiebreak hypothesis (b) is further deprioritized: per-row
agreement leaves no room for a tie — PG is genuinely cheaper
somewhere in level 2, and the R91 inputs are the remaining unknowns.
Rows/widths stay ruled out.

Implementation needs its R97+ slice SCOPE citing this note; the
step-1 TEMP is reverted and no production code changed.
