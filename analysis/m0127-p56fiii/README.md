# M0127-P5.6-f-iii — the DS05 TIMEOUT did not hop, it was moved

**Date:** 2026-08-05 · **Tree:** `096d3949` · **Verdict:** the filed
"sweep-tail confound" hypothesis is **REFUTED**. The 2026-08-04 TIMEOUT change
is a real, reproducible, code-caused re-pricing introduced by
**`ce027cee` (M0127-P5.6-f)**.

## 1. What was filed, and why it looked like noise

The item recorded that the SF0.5 gate's single TIMEOUT "hopped from Q72 to Q47
(2026-08-04), unattributed", with the summary line identical across the
boundary (`PASS=94 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=1 SKIP=4`), and
attributed it provisionally to the documented sweep-tail confound (a server
that just ran a timeout query sits at GOMEMLIMIT with `GOGC=off` and thrashes).
Swings in both directions plus a hopping victim are exactly that signature.

They are also exactly the signature of a re-pricing. The summary line cannot
tell the two apart, because it counts timeouts without naming them.

## 2. The timeline says "step function", not "noise"

Per-query extraction over every full sweep in the results dir (`>=90` rows):

| sweep | commit | Q47 | Q53 | Q57 | Q72 |
|---|---|---|---|---|---|
| 20260804-001828 | 2919f868 | PASS 30s | PASS 28s | PASS 16s | TIMEOUT 308s |
| 20260804-021249 | 6b4492d7 | PASS 29s | PASS 28s | PASS 15s | TIMEOUT 311s |
| 20260804-031147 | 23c47807 | PASS 31s | PASS 28s | PASS 16s | TIMEOUT 314s |
| 20260804-040859 | 23c47807 | PASS 30s | PASS 28s | PASS 15s | TIMEOUT 313s |
| 20260804-050727 | af39527b | PASS 28s | PASS 29s | PASS 18s | TIMEOUT 315s |
| 20260804-192224 | 88b47448 | PASS 31s | PASS 26s | PASS 15s | TIMEOUT 329s |
| 20260804-203837 | a7d800b8 | PASS 30s | PASS 26s | PASS 18s | TIMEOUT 327s |
| 20260804-214607 | 30293f78 | PASS 31s | PASS 28s | PASS 15s | TIMEOUT 328s |
| **— boundary —** | | | | | |
| 20260804-232914 | 29daeb72\* | **TIMEOUT 332s** | PASS 6s | PASS 81s | **PASS 166s** |
| 20260805-012306 | ce027cee | TIMEOUT 333s | PASS 7s | PASS 86s | PASS 174s |
| 20260805-051839 | 0c6d135e | TIMEOUT 328s | PASS 6s | PASS 82s | PASS 161s |
| 20260805-065245 | 483f6ca4 | TIMEOUT 329s | PASS 7s | PASS 84s | PASS 161s |

Eight consecutive sweeps in the old regime, four in the new. Within-regime
spread is ±3 s. A GC/state confound is noisy and unrepeatable; this is a clean
step at one boundary, held across four commits and eight hours on each side.

Two structural points kill the confound reading outright:

* **Q47 runs at position 47, before Q72.** In the old regime no timeout had
  occurred when Q47 ran; in the new regime Q47 *is* the first timeout. The
  sweep-tail confound cannot reach Q47 in either regime.
* `RESTART_AFTER_TIMEOUT=1`, so the post-Q47 restart is what makes Q53 (6 s)
  and Q72 (166 s) look better — but a *fresher* server cannot explain Q57
  getting 5× slower.

## 3. Measured, not argued: solo runs on a quiet host

Host verified quiet (load 0.26, no nightly, no stray servers). Fresh S-cold
server, live SF0.5 cluster, `TIMEOUT_SEC=900` so the true runtime is recovered
rather than clipped at the 300 s cap:

```
QUERIES="47 57" TIMEOUT_SEC=900 SF05_RESULTS_DIR=analysis/m0127-p56fiii \
  GOOPG_BIN=$PWD/tmp/goopg-p56fiii-bin scripts/tpcds-sf05-regression.sh sweep
```

→ `Q47 PASS 523s 100 rows` · `Q57 PASS 81s 100 rows`
(`sweep-20260805-082026.txt`, `plans-20260805-082026.txt`)

Both reproduce their **post**-boundary timings in isolation. The confound
hypothesis predicted Q47 ≈ 31 s outside the sweep tail. It is 523 s. Refuted.

## 4. Bisect — on a copy, so the live cluster was never at risk

`bench/tpcds/runtime_goopg/data-sf05` (2.3 G) was copied to `/tmp` and every
old-binary probe ran against the copy; a worktree at the target commit built
each binary. Same data dir for every cell, so code is the only variable.

| binary | Q47 plan (top join) | Q47 runtime |
|---|---|---|
| `30293f78` (P5.6-e-iii) | **Hash Join**, 5-pair `Hash Cond` | **31 s** |
| `29daeb72` (P5.6-f-pre) | identical to `30293f78` — byte-for-byte | **30 s** |
| `096d3949` (HEAD) | **Nested Loop**, *no join condition* | **523 s** |

So **`29daeb72` is exonerated on both plan and runtime**, and the old binary on
*today's* data is fast — which exonerates the cluster data too. The change is
in code, after `29daeb72`.

`29daeb72..ce027cee` contains exactly **one** commit:

```
ce027cee planner(cost): M0127-P5.6-f — two clauses priced as one, and the
                        composite key that stops the fix from overshooting
```

P5.6-g-i's four committed corpus captures corroborate it for free: Q47's top
join is already the degraded `Nested Loop` in **A=ce027cee**, B, C and D alike,
while `30293f78` still has the `Hash Join`.

### Why the boundary sweep is labelled `29daeb72`

Its own header says so:

```
# goopg: 29daeb72 catalog(durability): M0127-P5.6-f-pre — ...
# build: rebuilt from tree [tree DIRTY in Go sources — the binary is not this commit alone]
```

`diff=129e691bd41a` (non-empty). That sweep's binary was `29daeb72` **plus
uncommitted P5.6-f WIP**, later committed as `ce027cee`. The header records
HEAD, not the tree — which is precisely why it also prints the dirty-tree
warning and a content hash. The warning was right and was not read.

## 5. Mechanism

Q47's outermost join carries **five** equi-pairs:

```
Hash Cond: ((i_category = item.i_category) AND (i_brand = item.i_brand)
        AND (store.s_store_name = s_store_name)
        AND (store.s_company_name = s_company_name) AND (rn = (rn - 1)))
```

P5.6-f changed pricing from one pair to every pair
(`internal/planner/cardinality.go:457-483`), folding them under **independence**:

```go
for i, p := range pairs {
        ...
        if nd := pairNDistinct(j, p); nd > 0 {
                sel /= float64(nd)      // multiplied across ALL pairs
```

Two of those pairs are strongly correlated (`i_category`↔`i_brand`,
`s_store_name`↔`s_company_name`), so dividing by each pair's ndistinct in turn
under-estimates the joinrel by orders of magnitude. A sufficiently tiny inner
estimate makes a nested loop look free; at runtime the CTE carries real volume
and the join is quadratic. The same commit's win on Q9/Q72/Q53 is the *same*
mechanism pointed the right way — those joins' pairs really are closer to
independent.

This is the **inverse** of the known single-key degeneracy trap: the fix for
under-pricing over-corrected into under-estimation.

Per the P5.6-g-vi rule, the structural claims above (join method, presence and
arity of `Hash Cond`) are filter-independent and safe; the `rows=` figures on
`Filter:`-carrying lines in these captures are not quoted as evidence.

## 6. What PG does that goopg does not

> **⚠ RETRACTED 2026-08-05 — see `analysis/m0127-p56fiv/README.md`.** The
> paragraph below is wrong. `clauselist_selectivity_ext` gates the whole
> extended-statistics branch on `find_single_rel_for_clauses`, which returns
> `NULL` as soon as any clause has two relids — so
> `dependencies_clauselist_selectivity` **never runs on a join clause list**.
> PG multiplies multi-pair join clauses blind, exactly as goopg does, and
> measured on the SF0.5 oracle PG estimates Q47's two correlated 5-pair joins
> at `rows=1` itself. The real divergence is a **425× under-estimate of the
> `v1` subtree** (goopg 18 rows vs PG 7 643), which is what makes the nested
> loop look free; it traces to a pushed-down restriction being charged a second
> time at the join above it. Sections 1–5 above are unaffected.

~~PG does not fold a multi-column clause list under independence when it has
better information: `clauselist_selectivity`
(`postgres/src/backend/optimizer/path/clausesel.c`) consults extended
statistics first — `dependencies_clauselist_selectivity` and
`statext_clauselist_selectivity`
(`postgres/src/backend/statistics/dependencies.c`, `extended_stats.c`) — and
`get_foreign_key_join_selectivity` (`costsize.c:5651`) short-circuits the
multiplicative collapse when the pairs are an FK's columns. goopg has the FK
arm (P5.6-f landed it) but no functional-dependency arm, so a correlated
non-FK composite still multiplies out. Ledger row dated 2026-08-05.~~

## 7. Verdict on the filed item

* The TIMEOUT does **not** hop without a code change. Asked and answered
  without the three ~1 h sweeps the resume point proposed.
* Correctness never moved (`PASS=94 MISMATCH=0 CKMISMATCH=0` throughout, Q47
  returns its 100 oracle rows in every regime). This is cost, not answers.
* P5.6-f is a **net** win that was accepted on a TPC-H estimate audit reporting
  "no joinrel worse". On DS05 it is +Q72 (timeout→166 s), +Q53 (28→6 s),
  −Q57 (15→81 s), −Q47 (31→523 s). Both directions are real.
* The gate hid it because `TIMEOUT=1` is invariant to *which* query timed out.
  P5.6-g-i-b's plan-shape channel now covers plan drift; a **named**-victim
  comparison would have caught this one on the night it landed.
