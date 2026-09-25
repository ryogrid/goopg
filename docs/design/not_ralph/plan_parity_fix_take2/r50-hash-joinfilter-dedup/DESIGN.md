# R50 — hash-`Join Filter:` conjuncts already covered by `Hash Cond:` (DESIGN, 2026-09-10)

*Step-0: DS-Q56. Witnesses in this dir: `goopg-q56-r50.txt` (goopg: HC +
identical JF ×3) vs `pg-q56.txt` (PG: HC-only on the same semi joins).
Slice plan + gate report: `SLICE-A.md`.*

## 0. Contract

PG never duplicates a hash clause into `joinqual`: `create_hashjoin_plan`
builds joinqual as `list_difference(joinclauses, hashclauses)`, so an
all-equi hash join prints NO `Join Filter:` line, and a mixed join prints
only the genuinely non-equi remainder (verified: PG Q56/Q59 HC-only;
PG Q47/Q58 `MC(item_id)` + `JF(ranges)`). goopg's stated rule is the same
— "every conjunct prints exactly once"
(`explain_join_cond_test.go:143`, `TestExplainNoJoinFilterWhenKeysCoverThePredicate`) —
but 26 `Join Filter:` lines over 10 TPC-DS queries print conjuncts their
sibling `Hash Cond:` already shows (TODO.md R50 adjudication, class (b)).

Slice A restores the rule by admitting the bpchar family into the
hash-safe set. NO new subtraction rule, NO renderer change, NO new
executor path: the P2.2 machinery (`ExecHashKeyPlan`, `join_exec_keys.go`)
already folds safe pairs into the key encoding AND subtracts them from
the residual. The whitelist is the only thing standing in the way.

## 1. Mechanism (traced, not assumed)

- EXPLAIN: `formatJoinKeyCond` (`operators_explain.go:973`) renders ALL of
  `p.HashKeys` (the plan's truth). `formatJoinFilter` (`:1018`) renders
  `ExecHashKeyPlan().Residual` for hash joins.
- `ExecHashKeyPlan` (`join_exec_keys.go:62`): `Keys` = lead pair
  unconditionally + hash-safe rest; `Residual` =
  `residualExcluding(safe)` — Predicate minus conjuncts matching a SAFE
  pair via `conjunctIsOneOfKeys` (`join_hash_keys.go:249-287`).
- The dup shape is therefore exactly: pair ∈ HashKeys (shown in HC) but
  ∉ safe (kept in JF + re-evaluated per match by `joinPredicateMatchSlot`,
  `operators_join_agg.go:1370`). Two sub-shapes: lead-but-unsafe
  (Q56/Q60/Q24-zip/Q59-store_id/Q4/Q11/Q74-customer_id/Q58-item_id — in the
  encoding via Keys[0], double-evaluated) and non-lead-and-unsafe
  (Q47/Q57 brand — NOT in the encoding; the residual is its ONLY
  enforcement today).
- Census of the discriminator (2026-09-10): every in-scope pair is
  bpchar-family — `character(16/50/10)` base columns (PG
  `information_schema` on `:65438`/tpcds05: `i_item_id`, `s_store_id`
  character(16); `i_category`, `i_brand` character(50); `ca_zip`, `s_zip`
  character(10)) or CTE outputs with propagated char types (Q4/Q11/Q74
  `customer_id`, Q47/Q57 category+brand, Q58 `item_id`). The Q47 control
  is decisive: same join keeps category+brand (character) while
  subtracting `s_store_name`/`s_company_name` (**varchar(50)** — already
  whitelisted) and `(rn-1)=rn` (numeric — already whitelisted).
- Probe matrix (`zz_r50_probe_test.go`, throwaway): char-base DUP,
  varchar-base CLEAN, char-CTE DUP, varchar-CTE CLEAN. So CTE output
  schemas propagate types correctly — the discriminator is PURELY the
  type-name whitelist, not type loss. Runtime `Type.Name` for `char(16)`
  DDL is `"char"` (probe-logged from the catalog).
- Spelling coverage: `char(16)` DDL keeps display name `"char"`;
  `character(16)` keeps `"character"` (`parser/ddl.go:5395-5430`,
  display names deliberately NOT collapsed); internal `bpchar` stays
  `"bpchar"`. All three name ONE family everywhere else
  (`catalog.go:1155`, `PadBpchar`, `codec.go` text-like arms) — the
  whitelist must admit all three, exactly as it already admits both
  `"text"` and `"varchar"`/`"character varying"`.

## 2. Safety: why folding bpchar into the key is values-neutral

The residual re-check after a hash match can only change results on rows
that MEET in a bucket. Meeting requires byte-identical composite-key
encodings; each part is `datumKey` (`operators_join_agg.go:4530`), a
deterministic function of the datum. Dropping a conjunct is therefore
newly-wrong ONLY IF two datums can be datumKey-equal yet `=`-false
(direction 2). Direction 1 (`=`-true yet keys differ — rows never meet)
is unreachable by any residual and identical with or without this slice.

For KindString `datumKey` is `"s:" + raw bytes` — collision ⟺
byte-identical ⟹ `=`-true, since goopg's string `=` is `compareDatum==0`
(`expr.go:2013-2018`) and every KindString normalization there (pg_lsn,
UUID, `(…)` row-literal, `{…}` array-literal) is reflexive on identical
input. (Review note 1: the direction-1 converse is scoped, NOT
universal — see the UUID caveat below.)

- Width-carrying bpchar (all in-scope base columns): stored TRIMMED
  (`codec.go` `coerceTextLikeDatum`; "no distinct padded representation",
  `expr.go:1144-1146`). No trailing spaces survive to distinguish, so
  datumKey-equality and `=`-equality coincide exactly on stored values.
- Unbounded bpchar / non-coerced paths (literals, function results, CTE
  verbatim): stored VERBATIM. `'AB'` vs `'AB '` get different keys and
  never meet — the same miss the residual produces today (residual-false
  ⟺ no emit; no-meet ⟺ no emit). Byte-identical keys ⟹ `=`-true.
- UUID normalisation in comparators is a pure function: same bytes ⟹
  same normalised form ⟹ equal. NULL keys never meet
  (`encodeCompositeKey` ok=false) and NULL `=` is never true — no change.
- Non-reflexive operators (float NaN) are the reason the whitelist stays
  deny-by-default: float4/float8 are NOT admitted by this slice (their
  datumKey arm all-collides — residual does ALL the work there; dropping
  it would be catastrophic and no in-scope pair is float).

Jointype scope: all 26 lines are INNER or SEMI (census). Rows reaching
the residual already satisfied every ENCODED key; after folding, the
dropped conjunct's pair is itself encoded, so its check is redundant by
the argument above. No ANTI line in the 26-line census (review note 4:
`ExecHashKeyPlan` applies the whitelist to anti joins too — only
`NullAware` is force-single-keyed — and the soundness argument covers
them identically; the gate's zero-other-diff check binds any future
anti-JF movement).

Review-note 1 caveat (direction-1 scope): for a NON-LEAD unsafe pair
that newly joins the encoding (Q47-brand shape), the residual today
*rescues* direction-1 misses where `compareDatum` normalizes
byte-different datums to `=`-true (UUID-case/format variants,
pg_lsn-shaped, `(…)`/`{…}`-leading strings). After folding, such rows
never meet (miss). This is corpus-harmless (no in-scope values have
these shapes) and PRE-EXISTING for already-whitelisted `varchar`/`text`
(the same folding was accepted at P2.2) — the proof claims parity with
the varchar status quo, not universality. Pinned by the UUID-case-variant
direction-1 test (§5).

Review-note 2 (`"char"` alias): admitting `"char"` also admits the
quoted OID-18 single-byte `"char"` (same `Type.Name` post-parse).
Harmless — same-name-both-sides plus byte-exact `datumKey`/`=` under
either representation — and `char[]` stays excluded via the `IsArray`
guard in `pairIsHashSafe`.

Shared-predicate effect: `pairIsHashSafe` also governs
`ExecMergeKeyPlan`. The merge comparator (`compareDatum`) raw-compares
strings, which on trimmed storage coincides with `=`. No in-scope line
is a merge join; the values gates bind the shared path.

No ANALYZE movement: `joinFilterRemoved` counts only residual REJECTS;
HC-covered conjuncts always pass under the agreement above, so the
counter is unchanged (contrast R49 Slice B's join→scan attribution move).

Deform: the residual SHRINKS, so `deformJoinBounds`
(`scan_deform.go:481`) narrows — the safe direction under fail-closed
full defaults.

Cardinality untouched: `cardinality.go:1056` excludes by the FULL pair
list, not the safe subset — no overlap with this change.

## 3. Slice-A scope (28 lines / 11 queries; census refs in TODO.md R50)

- Exact-dup, line vanishes: Q56×3, Q60×3 (`item_id` semi); Q24×2 (zip;
  goopg stays hash vs PG's NL — doctrine over coincident text, query
  stays shapediff per adjudication); Q4×3/Q11×2/Q74×2 pure-dup arms;
  Q83×2 (`item_id` over char-typed CTE outputs — the TODO survey filed
  this as "JF-only, no companion HC", but the corpus shows
  `Hash Cond: (item_id = item_id)` + identical JF on the SAME node, and
  PG's Q83 carries zero `Join Filter:` lines: textbook class (b), in
  scope; gate corpus `oc-ds05-r50a.txt` confirms HC-only).
- Full vanish, not the predicted shrink: Q47×2, Q57×2. The design draft
  predicted the lines would keep the numeric `(rn∓1)=rn` remainder, but
  the gate A/B shows the whole line gone — the remainder was NEVER in
  JF (varchar `s_store_name`/`s_company_name` + numeric already folded
  pre-slice; the JF carried only category+brand, per the Q47 control).
  Outcome is strictly closer to PG (no duplicated hashclause) than the
  prediction.
- Subset, line shrinks to the non-equi remainder: Q59×1 (drop store_id,
  keep week-arithmetic); Q58×2 (drop item_id, keep ranges);
  Q4×2/Q11×1/Q74×1 (drop customer_id, keep CASE ratio); Q24 `<>`
  conjuncts stay (non-equi, PG-identical role).
- Step-0: DS-Q56 first semi join — HC+JF → HC-only, values bit-identical.

## 4. Non-goals (TODO.md follow-ups, NOT this slice)

Q13H/Q48H
(state/profit OR placement); NL-equi probe-ability (Q37-class); Q5-class
join-choice; float/bpchar-verbatim direction-1 misses (values work, if
ever — PG would match `'AB'='AB '`, goopg misses with AND without this
slice; unchanged).

## 5. Pins (tests first where a text/contract is pinned)

- Whitelist unit (`internal/optimizer`, beside P2.2 pins): char/bpchar/
  character pairs hash-safe (int-family precedent shape); float still
  unsafe; bpchar[] still unsafe (IsArray guard).
- Probe graduation (`zz_r50_probe_test.go` → `hash_join_bpchar_test.go`;
  landed): char-base dup GONE (HC-only, byte-exact HC text), varchar
  unchanged, char-CTE gone, NULL-key char join still matches nothing,
  exact counts, a trailing-space pair `('ab' vs 'ab ')` documenting
  keys/`=` AGREEMENT (unbounded bpchar stores verbatim, goopg's `=` is
  byte-exact on strings, so scalar `=` is false and the join is empty
  both pre- and post-slice — NOT a direction-1 instance), PLUS a
  UUID-case-variant char pair (`'A8098C5A-...'` vs lowercase —
  `compareDatum` normalizes to equal) documenting the review-note-1
  direction-1 miss parity: the pair met via the residual pre-slice
  (count 1) and misses post-slice (count 0), identically for `varchar`
  (status-quo control, folded since P2.2) and `char` (new) — i.e. the
  slice introduces no NEW behavior class. (No lossy-hash pin: hash has
  no `tbmLossify`-style page-degradation path, so exact counts bind the
  residual-narrowing.)
- Multi-key partial pin (landed): two-pair char+numeric join folds BOTH
  (HC shows both, no JF); char-equi + non-equi keeps exactly the
  non-equi remainder in JF (Q59-shape doll-house).
- End-to-end render pin (landed): HC text asserted byte-exact per shape
  (Keys membership for the lead is unchanged; non-lead-unsafe JOINS
  Keys — HC renders HashKeys, not Keys, so HC text is invariant by
  construction).

## 6. Gates

- Census A/B on `:5533`/`:5534` clone discipline (GUCs pinned): 26 target
  lines vanish/shrink; ZERO other plan-text diffs (HC-extra explicitly
  NOT in scope — any HC change fails the gate); orphan/attribution
  machine check with the fixed alternation pattern (TODO.md trap note).
- TPC-H canonical digest 24/24 MATCH + Q12=2/Q13=34; TPC-DS SF0.5 sweep
  all-zero (deferred sweep binds here — executor behavior changes).
- Units: `internal/optimizer` + `internal/executor` green (no `-count=1`).

## 7. Landing

ONE commit: the change is a single predicate (`isHashSafeTypeName`)
consumed by BOTH the renderer-side residual and the executor-side
encoding decision — planner-without-executor / executor-without-planner
cannot exist separately. No split-brain. OP: admit the three spellings
with a comment citing the trimmed-storage agreement (§2) and the
float/bpchar[] exclusions that remain.
