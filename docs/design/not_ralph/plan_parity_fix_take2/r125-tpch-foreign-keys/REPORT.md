# R125 result — goopg now consumes `NOT VALID` foreign keys as PG does. A documented prior decision is REVERSED, deliberately.

Small, source-level, PG-cited. **No corpus change, no plan movement**
(neither corpus declares any FK today, so P0 is bit-identical by
construction) — this is step (a) of the four-step chain to Q9, and it is
the step that makes the rest affordable.

## 1. The defect

PG's planner never consults `convalidated`:

```c
/* skip constraints currently not enforced */
if (!cachedfk->conenforced)
    continue;
```
`get_relation_foreign_keys`, `plancat.c:642-644`. `RelationGetFKeyList`
(`relcache.c:4769-4776`) filters on `contype == CONSTRAINT_FOREIGN` and
carries `conenforced` only — `convalidated` never reaches
`ForeignKeyCacheInfo`. **PG feeds a `NOT VALID` FK straight to
`get_foreign_key_join_selectivity`.**

goopg refused it at two sites — `joinrelsize.go:658` and
`joinkeyproof.go:740`, both `if fk.NotValid || fk.NotEnforced || …` — a
straight parity divergence inside a function otherwise ported
faithfully. Both now gate on `NotEnforced` alone.

**The omission is deliberate upstream, not an oversight** (found in
review): PG filters on `convalidated` where it means something —
`CheckConstraintFetch` skips unvalidated CHECK constraints
(`relcache.c:4635`) — and pointedly does not in the FK path.
`grep convalidated postgres/src/backend/optimizer/` returns zero hits.

## 2. Gate 1: the consumer enumeration, done BEFORE the edit

The SCOPE required this because the two sites are not obviously
equivalent: one feeds a selectivity, the other is named a *proof* site,
and the shared struct carries something documented as a **"STRUCTURAL
upper bound"**. PG derives no such bound from `fkey_list`, so honouring
an unvalidated FK there could have been unsound where it is not in PG.

Every consumer, enumerated:

| consumer | what it does | a guarantee? |
|---|---|---|
| `cardinality.go:678-679` | `rows = min(rows, sk.rowsBound)` | **no** — clamps a row estimate |
| `joinrelsize.go:250-251` | `rows = min(rows, est.rowsBound)` | **no** — same clamp |
| `cardinality.go:639` | `measured := sk.fired \|\| sk.boundProven`, gating whether a default selectivity is substituted | **no** — estimate-only |

The SCOPE also asked specifically about **executor sizing and memory
allocation**, and the first draft of this table stopped at the optimizer
boundary. Following the joinrel `rows` outward:

| downstream consumer | what it does | a guarantee? |
|---|---|---|
| `executor/operators_join_agg.go:786-794` → `hashsize.Choose` | presizes the hash-build map | **no** — uses `NBuckets` only, and the map still grows; `NBatch` is computed and ignored today |
| `executor/subq_cache.go:106` `corrSubqHashMapReserve` | reserves a cache map | **no** — reconciled against the map's measured size after build, falling back to the always-correct rescan path |
| `memoize.go:128`, `pushdown.go:325` | plan choices | **no** |

**No consumer treats the bound as a guarantee.** Nothing allocates
irrevocably from it, elides work, or derives a semantic transform. So a violated
constraint costs an optimistic *estimate*, never a wrong answer — which
is exactly the risk PG already accepts, because an FK-derived
selectivity itself assumes each child row matches exactly one parent,
the very property an unvalidated constraint may violate.

Verdict: relax **both** sites. The SCOPE's hedge ("likely shape is relax
the selectivity site, keep the guard on the proof site") was the
cautious guess; the enumeration says the distinction does not exist in
the consumers.

## 3. A prior decision is reversed — stated, not buried

`TestCalcJoinrelSizeInvalidFKIgnored` existed and asserted the opposite,
with a reasoned comment: *"a NOT VALID / NOT ENFORCED constraint proves
nothing about the rows already in the table, so it must not license a
no-fan-out estimate."*

**That reasoning is correct about proof and wrong about PG.** It is the
right instinct applied to the wrong question: the FK here does not
license a proof, it licenses an estimate.

And the strongest answer is not "parity overrides safety" — it is that
**the safety concern is quantitatively identical to PG's**, a point the
first draft missed and review supplied. A NOT VALID FK is still
**enforced against every new row**, in goopg exactly as in PG: runtime
enforcement gates on `NotEnforced` only (`operators_fk.go:118`, `:170`),
never on `NotValid`. So the unchecked set is the pre-existing rows
alone, and only until `VALIDATE CONSTRAINT`. Both engines' planners are
optimistic over the same finite, shrinking set.

A residual naming tension remains and is worth a follow-up: `boundProven`
/`rowsBound` promise a proof the FK arm never delivered (PG derives no
bound from `fkey_list` at all). The right fix is to rename them, not to
restore the guard.

The pin is therefore **renamed and inverted**, not deleted —
`TestCalcJoinrelSizeInvalidFKHonouredLikePG` — carrying the PG citation,
the enumeration above, and an explicit failure message naming the
pre-R125 behaviour. Its surviving half is split into a new
`TestCalcJoinrelSizeNotEnforcedFKIgnored`, because `NOT ENFORCED` is the
filter PG actually applies.

A reviewer who disagrees with reversing it should say so: this is the
round's only judgement call.

## 4. Results

| # | bar | result | verdict |
|---|---|---|---|
| P0 | no behaviour change with no `NOT VALID` FK present, **both corpora** | TPC-H **byte-identical** (md5 `19b1c9a1…` both). TPC-DS captured too (`ds-r125.plans.txt`) and **bit-identical** to R124's OFF. goopg `tpch` has **no constraints of any type** (`pg_constraint` → 0 rows) and `tpcds025` has 0 FKs, so nothing could move | **PASS** |
| P1 | a `NOT VALID` FK reaches the estimator | new pin, real planner, Q9-shaped 2-column join: **noFK=8000, validFK=40000, notValidFK=40000** — NOT VALID now behaves exactly as validated, and the FK arm corrects a 5x under-estimate to exact (every child row matches one parent). **Mutation-tested**: restoring the guard makes it fail with `notValid=8000 valid=40000` | **PASS** |
| P2 | every `boundProven`/`rowsBound` consumer enumerated, per-site decision justified | §2 | **PASS** |
| P3 | suites green | `internal/optimizer` + `internal/executor` green, `go vet` clean | **PASS** |
| gates | per CLAUDE.md a planner change owes the spotcheck | `scripts/tpch-spotcheck.sh` **RESULT=PASS** (Q12=2, Q13=34) | **PASS** |

**On the values gates, stated as a judgement rather than a pass:** the
TPC-DS SF0.25 values sweep was **not run**. The reasoning is that a
change which provably cannot alter a plan on either corpus (P0,
byte-identical on both) cannot alter a value — but that substitutes an
argument for a documented gate, and it is recorded as such.

## 5. What this does NOT do

**Q9 does not move, and cannot yet.** This round declares no FK on the
bench corpus. TPC-H stays 6/22, TPC-DS 2/99.

Remaining chain to Q9, unchanged from the SCOPE:

- **(b) persist FK constraints across restart** — the blocker. Proven
  empirically: create an FK, `pg_constraint` shows it, CHECKPOINT, stop,
  start → **0 rows**, data intact. `catalog.Table.ForeignKeys`
  (`catalog.go:641`) is the only store, `pg_constraint` is *synthesised*
  from it (`:7248`), and startup has no reload path — so the reload must
  repopulate that field, not the view.
- **(c) index path for FK validation** — currently O(child × parent)
  full heap scan (`operators_ddl.go:9121` →
  `validateFKConstraintExistingRows` → `scanRelForFKMatch`), measured
  **32.17 s for 40,000 × 8,000**, extrapolating to ~2 weeks for TPC-H's
  eight. **This round makes (c) optional rather than blocking**: with
  `NOT VALID` now consumed, the eight FKs can be declared in O(1) and
  still feed the estimator, exactly as they would on PG.
- **(d) declare them and measure Q9.**

Carried for (d), and worth not re-deriving: with the FK firing, goopg's
Q9 estimate is *computable* — `40,132 × 6,001,255 / 800,000 ≈ 301,050`
(not PG's 363,341; PG's `partsupp ⋈ part` is 48,484 where goopg's is
40,132). Ground truth is 318,748. And "Q9 → MATCH" remains
**speculative**: on the reviewer's synthetic reproduction the estimate
corrected exactly while the plan shape did not change at all.

## 6. Disposition

No flag — and the reasons are worth stating, since every other round in
this series shipped behind one:

- A default-off flag here would be **dead code**: no corpus declares an
  FK, so the flag could never be A/B'd. That is exactly the 5-way-A/A
  trap R124 fell into.
- The **declaration itself is the A/B knob** for step (d): declare vs
  not, `NOT VALID` vs validated. Isolation is not lost.
- The flag norm exists to make a *behaviour* toggleable; this behaviour
  is already gated on evidence that does not exist yet.

## 7. Artefacts

P0 pairs: `r124off.plans.txt` vs `r125.plans.txt` (TPC-H, byte-identical)
and `ds-r124off.plans.txt` vs `ds-r125.plans.txt` (TPC-DS, bit-identical).

**Unreproducible, and load-bearing — capture before relying on them
again.** §5's "FK persistence lost across restart", "32.17 s for
40,000 × 8,000" and "~2 weeks for the eight" were produced on a
throwaway cluster during review and exist only as prose. The persistence
claim is corroborable structurally (`catalog.go:641` is the only store,
`:7248` synthesises `pg_constraint` from it, and no startup path
repopulates it). The two **timing** figures are the input to the
decision that step (c) is optional, so step (b)/(c)'s scope should
re-measure and commit the artefact rather than cite this report.
