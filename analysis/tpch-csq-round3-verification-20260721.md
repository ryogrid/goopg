# Correlated-Subquery Planning Round 3 — Closing the Open Ledger Rows

**Date:** 2026-07-21
**Branch:** `planner-kaizen3` (base `872b424d`, on top of round 2's `planner-kaizen2`)
**Design:** [`docs/design/correlated-subquery-planning/09-round3-open-items.md`](../docs/design/correlated-subquery-planning/09-round3-open-items.md)
**Stage tracking:** [`IMPLEMENTATION-TODO.md`](../docs/design/correlated-subquery-planning/IMPLEMENTATION-TODO.md) (Round 3 part)
**Round-2 report:** [`tpch-csq-round2-verification-20260721.md`](tpch-csq-round2-verification-20260721.md)

Round 3 closed the five rows round 2's report §6 listed as still open. Unlike
rounds 1 and 2 — which implemented a designed roadmap — this round's work was
**investigation-led**, and the investigation changed three of the five
framings before any code was written. That is the round's main result: two
of the five "deferred optimisations" were live wrong-results bugs, and one
"wrong-results bug" was not reproducible at all.

## 1. What each row turned out to be

| ledger row (as filed) | what it actually was | outcome |
|---|---|---|
| LEFT+residual NLI hazard — *"latent, audit it"* | **live wrong-results bug**: the planner emits the hazardous shape today through two ungated routes | fixed (R3-1) |
| hashed-probe family limits — *"widen an allowlist"* | **live wrong-results bug in hash JOINS**, one level below the probe | fixed (R3-3) |
| derived-table zero-rows — *"wrong results, needs an executor loop"* | **not reproducible**; the stated mechanism is structurally impossible | falsified + pinned (R3-2) |
| composite-equijoin EXISTS — *"needs multi-column join keys"* | machinery already existed end-to-end; the bail was the only obstacle | implemented (R3-4) |
| DML-sublink lowering — *"extend host discovery"* | one missing walker case; **zero** executor plumbing needed | implemented (R3-5) |

## 2. Commit ledger

| commit | stage | content |
|---|---|---|
| `3656dbda` | R3-0 | design chapter (`09-round3-open-items.md`), investigation-backed, agent-reviewed |
| `bfcc2862` | R3-0b | review findings folded back in; staging reordered (correctness first) |
| `70792111` | R3-1 | NLI LEFT residual semantics: match-on-predicate-pass + unconditional null-pad; planner leak routes pinned |
| `d9cab218` | R3-3 | big-numeric hash keys (`canonicalBigNumericKey`); string-shape hazard refuted by measurement |
| `ef690880` | R3-2 | derived-table zero-rows falsified at HEAD + two regression pins |
| `566a08d8` | R3-4 | composite EXISTS/NOT EXISTS decorrelation; the missing Project-strip guard written |
| `bb90089a` | R3-5 | DML WHERE sublink lowering via a host-discovery-local wrapper |

## 3. The two live bugs, in detail

### 3.1 NLI LEFT-join residual (R3-1)

The ledger recorded this as latent because "the planner declines NLI for
LEFT+residual". That premise holds only for Q13's shape, and only
incidentally: Q13's ON residual is *inner-only*, so it becomes a
`Filter{inner}` that `pickInnerSide` declines. A **cross-relation** ON
residual is classified `sideMixed`, stays on the join predicate while the
equi pair is lifted into `LeftKey`/`RightKey`, and reaches `tryBuildNLI`
with a bare-SeqScan inner — where the leftover-retention path (and,
separately, OR-factoring) attaches it with no join-type gate at all.

The operator then had two defects, both the bug class the hash join fixed
in M0119-0004:

1. the match flag was set on row-**produced**, before the residual was
   evaluated, with a reset on failure for Anti only. LEFT could not use
   Anti's reset discipline — its loop revisits candidates after emitting
   one, so a reset would re-arm the fallback and duplicate the outer row.
   Fixed by recording the match on predicate-**pass**, which needs no reset
   and is correct for both.
2. the null-pad fallback was gated on evaluating the residual *against the
   null padding*. The NLI `Predicate` is the JOIN ON residual, and PG
   evaluates a join condition only against real inner rows.

Severity exceeded the ledger's description: `nliConsumedByProbe` matches
the probe key by **pointer identity** against a rebuilt keys slice, so the
retained residual usually still contains the equi conjunct. The padded row
then fails on that conjunct alone — meaning *every* unmatched outer row was
dropped, not only those whose residual touches an inner column. Measured:
the NLI path returned **1 of 4 rows** where the hash path returned 4.

### 3.2 Big-numeric hash keys (R3-3)

Filed as an optimisation (the hashed IN probe declined big-mantissa
numerics). The decline was covering a wrong-results bug one level down:
`datumKey`'s `KindNumeric` arm called `NumericMantissaValue()`
unconditionally, which is correct only on the int64 fast lane — in the big
lane that accessor returns the mctx `(offset<<32 | len)` encoding, not the
value. `datumKey` is not probe-local: it is the canonical key for
**equi-join build and probe sides** and for grouping. Two equal numerics
past int64 stored at different offsets therefore hashed differently and
their pair was silently dropped.

Measured on a 3-row equi-join over such values: **1 pair instead of 3**,
while `k = <big literal>` on the same rows was correct — which localised
the fault to the key rather than the value or `compareEq`. The correlated
IN form failed identically with the hashed probe both on and off, because
the planner decorrelates it into a semi join using the same key.

`canonicalBigNumericKey` applies the int64 lane's exact normalisation and
returns to it when the stripped mantissa fits, so the two lanes converge on
one key — which is what lets `hashFamNumeric` absorb the big lane without
splitting the family.

## 4. A refutation worth recording

The design review raised a plausible second hazard in the same area:
`compareDatum` applies UUID / pg_lsn / row-literal / array-literal
normalisation that `datumKey`'s plain `"s:" + value` does not, so the
hashed probe could miss where the linear loop matches.

**Measurement refuted it.** The probe's oracle is the linear loop, which
compares with `compareEq`, and `compareEq`'s string arm is a plain byte
comparison — it never reaches those normalisations, which are *ordering*
helpers. Probed on both paths with hyphen/case-varied UUIDs and `0/10` vs
`0/010` LSN strings: hashed ≡ linear. uuid-typed columns agree with PG too,
because goopg normalises at coercion time.

This is recorded rather than quietly dropped because the fix it called for
would have narrowed `hashFamString` and slowed a correct path to defend
against a bug that does not exist.

## 5. Measurements (SF1, guarded capped server, 600 s budget)

Before = round-2 close-out sweep (`sf1-r2-final-894485ba.txt`).

| Q | round 2 | round 3 | rows |
|---|---:|---:|---:|
| Q1 | 29.51 s | 29.06 s | 4 |
| Q2 | 2.52 s | 2.57 s | 459 |
| Q3 | 22.67 s | 22.54 s | 11175 |
| Q4 | 3.42 s | 3.39 s | 5 |
| Q5 | 416.46 s | 415.25 s | 5 |
| Q6 | 16.54 s | 16.21 s | 1 |
| Q7 | 150.77 s | 150.10 s | 4 |
| Q8 | 3.29 s | 3.76 s | 2 |
| Q9 | 100.08 s | 95.25 s | 175 |
| Q10 | 24.60 s | 25.33 s | 20522 |
| Q11 | 2.59 s | 2.58 s | 785 |
| Q12 | 27.59 s | 27.24 s | 2 |
| Q13 | 95.00 s | 96.91 s | 33 |
| Q14 | 47.29 s | 47.77 s | 1 |
| Q15-CREATEVIEW | 0.02 s | 0.02 s | 0 |
| Q15a-VIEWBODY | 17.90 s | 17.00 s | 10000 |
| Q15b-MAIN | 33.59 s | 33.36 s | 1 |
| Q16 | 6.27 s | 6.36 s | 18192 |
| Q17 | 47.77 s | 47.72 s | 1 |
| Q18 | 36.79 s | 36.88 s | 7 |
| Q19 | 52.00 s | 52.08 s | 1 |
| Q20 | 2.02 s | 2.04 s | 92 |
| Q21 | 27.75 s | 27.82 s | 370 |
| Q22 | 0.75 s | 0.78 s | 7 |

**24/24 slots complete, zero errors, and every row count byte-identical to
the round-2 sweep** — verified by diffing the two evidence files with the
timings stripped. Stream total ≈ **1 162 s** vs round 2's ≈ 1 167 s: a 0.4 %
difference, i.e. run-to-run noise, which is the correct result. Round 3
changed no TPC-H plan (plan-gate was 22/22 MATCH at every stage), so an
unchanged sweep is the *prediction*, and the numbers above are a
no-regression check rather than any kind of win.

The per-query spread (Q9 −4.8 %, Q8 +14 %, Q13 +2 %) is the usual variance
band on this machine; none of it tracks a code change. Evidence archived as
[`evidence/sf1-r3-final-bb90089a.txt`](../docs/design/correlated-subquery-planning/evidence/sf1-r3-final-bb90089a.txt).

**Why the SF1 sweep proves little about this round.** None of the five
fixed items occurs in TPC-H: no query has a LEFT join with a
cross-relation ON residual, none uses numerics past int64, none has a
composite-correlated EXISTS, and TPC-H issues no DML at all. The sweep's
job here is to show that fixing them broke nothing — the row-count and
plan-gate identities are the load-bearing evidence, and the correctness
claims rest on the new tests listed in §6.

## 6. Coverage added

- **Semantics matrix M1–M22 → M1–M26**: composite EXISTS enforcing both
  pairs (M23), composite NOT EXISTS with a NULL key column pinning
  plain-anti semantics (M24), composite plus a non-equi residual (M25), and
  a composite whose second pair excludes everything (M26).
- **NLI LEFT**: executor tests for both defects, including a cross-check
  that the NLI and hash paths now agree; planner pins for both leak routes,
  so a future planner change cannot silently orphan the executor tests.
- **Big numeric**: end-to-end join and IN-probe row counts, plus a
  key-level lane-convergence test.
- **Derived table**: both the ledger's NL-over-Unique shape (still
  reachable index-free) and the index-driven NLI shape SF1 takes today.
- **Composite EXISTS**: planner pins for predicate content, coordinate
  spaces, and the composite probe consuming both pairs; executor tests on
  indexed and unindexed shapes, cross-checked against the SubPlan path.
- **DML sublinks**: planner shape tests, a pin that `walkPlanExprs` still
  does not descend into `Update.Child`, and executor row-effect tests
  including two self-referencing Halloween cases — the second reads the
  column being written, so the pre-update-snapshot and moving-snapshot
  readings give different tables.

## 7. Deliberate exclusions (each ledgered)

- **`UPDATE … FROM` / `DELETE … USING` predicates, MERGE clauses, ON
  CONFLICT** stay on the legacy stack path: lowered Args there need
  combined-schema `SourceTableIdx` values. Guarded by a test, not left
  implicit.
- **Correlated IN keeps its own composite bail** — its operand-safety
  condition must be re-proven per pair.
- **The S6 Filter-inner unwrap still excludes LEFT**, but now for **cost**
  (the Q13 NOT-LIKE blowup would need D6.3b's `innerUnwrapCostAccepts`
  treatment), not correctness. The in-code comment was rewritten to say so.
- **Cross-family hash probing** (`10 IN ('10')`) stays declined: canonical
  numeric and text keys cannot meet without storing both forms per value,
  which defeats the O(1) probe.
- **`KindInterval` / toast pointers** stay unhashable pending their own
  verification.

## 8. Two stale artefacts corrected

Both were comments asserting a safeguard that did not exist — the most
expensive kind of documentation error, because it stops the next reader
from checking:

- the Project-strip in `unnestExistsExpr` claimed a "check whether the key
  index is accessible in the projected output" that was never implemented.
  R3-4 **wrote** it (bounds *and* name check per correlation column).
- a comment claimed that path's `RightKey` used inner-local indices "NOT a
  merged schema", contradicting the shift applied two lines below it. The
  *scalar* template genuinely does use inner-local indices, so the stale
  comment actively invited copying the wrong convention between paths.

## 9. Reproduction

```bash
scripts/csq-bench-server.sh start        # guarded + capped (wrapper defaults)
go build -o tmp/tpch-csq-runner ./cmd/tpch-runner
tmp/tpch-csq-runner --host 127.0.0.1 --port 65433 \
  --db postgres --user postgres --password postgres --per-query-timeout=600s
scripts/csq-bench-server.sh stop         # stop before the pgbench smoke gate
```

Round 3 adds no new kill switches; the round-1/2 switches
(`GOOPG_UNNEST_PREDP`, `GOOPG_NLI_COSTGATE`, `GOOPG_MEMOIZE`,
`GOOPG_INDEXKEY_HARVEST`, `GOOPG_SUBPLAN_RESCAN`, `GOOPG_HASHED_SUBPLAN`)
are unchanged.

**Gate note:** run the SF1 sweep and the pre-commit pgbench smoke
*sequentially*. One smoke run during this round aborted a transaction at
390 TPS while the capped bench server was still up; with it stopped the
same gate ran clean at 700 TPS.
