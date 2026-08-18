# M0134-0005 — `constraints.sql` divergence map + Buckets 1-4, 6, and the PREPARE/EXECUTE parameter path

Status: accepted — Buckets 1, 2, 3 and 6 LANDED 2026-08-18, plus BOTH halves of the
`get_nnconstraint_info` masking bug: the `reg*[]` cast (§7) and the declared-
parameter-type coercion at `EXECUTE` (§8), and Bucket 4's core fix (§10); the case
stays open (`[ ]`).
Running measurement: 1496 (baseline) → 1515 (B1, unmasking) → 1465 (B2) → 1431 (B3,
unmasking) → 1411 (B6, bucket interference) → 1411 (§7, stacked root cause) →
1376 (§8, the stack's upper half) → **1299** (§10, Bucket 4 statement-end tier);
hunks 30 → 30 → 31 → 33 → 33 → 33 → 34 → **36**.

## 1. Baseline (re-measured 2026-08-18)

`scripts/pg-regress-runner.sh constraints` at HEAD `7a272adb`:

| | diff lines | hunks (`grep -c '^@@'`) |
|---|---|---|
| before Bucket 1 | 1496 | 30 |
| after Bucket 1 | 1515 | 30 |

**Never compare against a pre-2026-08-18 `constraints` number** — those predate the
C19 harness fix (`-v HIDE_TABLEAM=on -v HIDE_TOAST_COMPRESSION=on`). Re-measure from
scratch.

The diff *grew* by 19 lines while a 13-occurrence error cascade *disappeared*
(`prepared statement "get_nnconstraint_info" does not exist`: 13 → 0). That is the
expected shape of an **unmasking** fix: statements that previously aborted with a
single short error line now execute and emit real (still partly wrong) result sets,
which occupy more diff lines. Diff line count is therefore not the metric of record
for this case; the named cascade being gone is.

## 2. Bucket map

Root-cause classification of all 30 hunks (research pass
`tmp/ralph-handoffs/m0134-0005-s01-measure/`). "Blocked" means the bucket's hunks
cannot be judged until an earlier bucket is fixed.

| # | bucket | size | goopg site | PG oracle | verdict |
|---|---|---|---|---|---|
| **1** | **PREPARE parameter-type validator rejects `regclass[]`** — and any array or typmod spelling, and whole built-in families | 13 hunks / ~120 lines | `internal/postmaster/dispatch.go:2038` `isValidSQLTypeName` | `gram.y` `PreparableStmt` takes `Typename` (carries `arrayBounds` + `typmods`); resolution is `parse_type.c:typenameType`, a real `pg_type` lookup | **LANDED** — see §3 |
| **2** | **`NOT ENFORCED` CHECK constraints are still enforced on INSERT** | ~4 hunks / 50 lines | `internal/executor/operators_fk.go:1664` `checkConstraints` loops `tbl.CheckConstraints` unconditionally, never consulting `tbl.NamedChecks[i].NotEnforced` | `execMain.c:ExecRelCheck` lines 1813-1815 — `/* Skip not enforced constraint */ if (!check[i].ccenforced) continue;` | **LANDED** — see §4 |
| **3** | **`DROP CONSTRAINT` / `RENAME CONSTRAINT` cannot find a NOT NULL constraint by name** — LANDED 2026-08-18, see §5 | ~6 hunks | `internal/executor/operators_ddl.go:10719` `execAlterTableDropConstraint` checks NamedChecks/FK/UNIQUE/EXCLUDE/PK but never `tbl.NotNullConstraints`; the RENAME sibling is **assumed by pattern, not read** | `tablecmds.c:dropconstraint_internal` handles `CONSTR_NOTNULL` alongside the other contypes | independent, bounded ⇒ good next slice; **verify the RENAME sibling before briefing** — verified 2026-08-18: the sibling DID share the omission. **LANDED**, §5 |
| **4** | statement-level / deferred UNIQUE checking during self-referencing UPDATE (`UPDATE unique_tbl SET i=i+1`, ring rotations, `SET CONSTRAINTS … DEFERRED` re-check timing) | ~6-8 hunks, was believed the largest line driver (~340 lines) | ~~not one function: goopg maintains unique indexes row-by-row inside UPDATE~~ — **wrong**: the DML sites already fan into one shared queue pair; the gap was a missing tier in `uniqueCheckDeferred` (`internal/executor/deferred_unique.go:45-53`) | `catalog/index.c:2080-2082` (`indimmediate=false` for ANY deferrable index) + `execIndexing.c:ExecInsertIndexTuples` `UNIQUE_CHECK_PARTIAL` + `unique_key_recheck` on the after-trigger queue | **LANDED** — see §10. **This row's "MILESTONE, real constraint-timing/MVCC work" sizing was REFUTED by research**: the milestone-sized machinery already existed (M0119-0004), and the fix was one slice. Sizing a bucket from the *symptom* rather than from the existing code is the error to avoid repeating. |
| 5 | `EXCLUDE USING gist` on `circle` → "data type circle has no default operator class", cascading to 10× "relation circles does not exist" | ~11 hunks | opclass registry has no circle/gist default opclass | PG ships `circle_ops` for GiST | **MILESTONE** (GiST opclass coverage) |
| 6 | `ALTER TABLE … ALTER CONSTRAINT <name> NOT VALID / INHERIT / NO INHERIT` unparsed (syntax error) | ~3 hunks | parser: no production for the standalone `ALTER CONSTRAINT` trailers (near `internal/parser/ddl.go:2863` `parseFKConstraintAttrs`) | `tablecmds.c:ATExecAlterConstrEnforceability` and the NO INHERIT toggle | parser gap is a small slice; the *semantics* of toggling inheritance on an existing not-null constraint is separate and larger |
| 7 | misc, **root cause not pinned** — `DEFAULT 123.456` on float8 truncated to `123` when the column is omitted from the INSERT; DEFAULT parsed as `a_expr` where PG uses `b_expr` (accepts `1 IN (1,2)`); a `currval()`-based default evaluating out of order relative to the `nextval()` default in the same row-fill | 2-3 hunks each, drives ~250 lines of INSERT_TBL | the missing-column *catalog-default fill* path — **distinct from** `rewriteInsertDefaultMarkers`/`defaultMarkerReplacement` (`internal/optimizer/planner.go:9860`), which only handles the explicit `DEFAULT` keyword | — | **do not brief from this row** without another researcher pass to pin the fill path |

**Cascade verdict:** bucket 1 was the only real cascade in the file — it owned 13 of
30 hunks and nothing else was blocked behind it. Buckets 2, 3, 6 are independent of
each other and of 4/5.

## 3. Bucket 1 — what landed

`isValidSQLTypeName` (`internal/postmaster/dispatch.go:2038`) is the validator behind
`PREPARE`'s declared parameter-type list; it answers the 42704 `type "%s" does not
exist` check at `dispatch.go:700`. It was a hand-written allowlist of **bare**
built-in names, so it rejected three orthogonal things PG accepts:

1. **array spellings** — `regclass[]`, `int4[][]`, `text[5]`. PG's grammar attaches
   `arrayBounds` to the `Typename`, and all dimensionalities resolve to the same array
   type; `_regclass` (OID 2210) is already seeded in goopg
   (`internal/initdb/pg_type_seed_data.go:119`).
2. **typmod spellings** — `varchar(10)`, `numeric(10,2)`, and the awkward
   `timestamp(3) with time zone`, where the parenthesised group sits *inside* a
   multi-word name.
3. **entire built-in families goopg already implements elsewhere** — most importantly
   the `reg*` family, which the codec and scan layers treat as first-class
   (`internal/executor/codec.go:402`, `internal/executor/reg_identifier.go:718`), plus
   `bit`/`varbit`, `inet`/`cidr`/`macaddr`/`macaddr8`, `money`, `xml`, `tsvector`,
   `tsquery`, `jsonpath`, `int2vector`, `oidvector`, `pg_snapshot`, `"char"`, and the
   SQL spellings `character` / `character varying`.

The fix normalises before matching: reject unbalanced parens, strip each balanced
`( … )` group wherever it occurs (re-collapsing whitespace), then strip trailing
`[]`/`[N]` groups (a non-numeric subscript is a rejection, not a strip), then match
the extended allowlist. Signature and call site are unchanged; the allowlist change is
purely additive, so no previously-accepted spelling can start failing. Pinned by
`TestIsValidSQLTypeName` (`internal/postmaster/prepare_param_type_test.go`), which
fails pre-change and passes post-change, and which asserts the rejections
(`nosuchtype`, `nosuchtype[]`, `""`, `int4(`) as explicitly as the acceptances.

**Deliberately not done:** replacing the allowlist with a real `pg_type` lookup. The
call site has `ectx.Catalog` in hand, so it is reachable, but the change would alter
the function's signature and pull user-defined type resolution (enum / domain /
composite) into a slice scoped to a cascade fix. Consequence: `PREPARE p (my_enum)` is
still a false 42704. Ledgered.

## 4. Bucket 2 — what landed (2026-08-18)

Research (`tmp/ralph-handoffs/m0134-0005-s03-not-enforced-check/report.md`) settled the
three questions that decided the shape of the fix:

1. **`NotEnforced` is already parsed, stored, and honoured everywhere except at
   runtime.** `catalog.NamedCheckConstraint.NotEnforced`
   (`internal/catalog/catalog.go:216`) is set by both `CREATE TABLE … CHECK (…) NOT
   ENFORCED` and `ALTER TABLE … ADD CONSTRAINT … NOT ENFORCED`. The ADD-CONSTRAINT
   initial data scan (`internal/executor/operators_ddl.go:8093`) already skips it, and
   `VALIDATE CONSTRAINT` already raises 55000 for it (`:8037`).
2. **The two slices cannot diverge.** `tbl.CheckConstraints` and `tbl.NamedChecks` are
   appended together in the single fan-in `catalog.AddCheckFull` /
   `AddCheckInherited` (`internal/catalog/catalog.go:288`), so they are index-aligned
   1:1 by construction. The fix indexes `NamedChecks` by the `CheckConstraints` loop
   index, with a defensive bounds check.
3. **There is exactly one runtime enforcement site** — `checkConstraints`
   (`internal/executor/operators_fk.go:1664`) is the single fan-in for INSERT/COPY
   (`operators_storage.go:2494`) and UPDATE (`checkRowConstraintsForWrite`,
   `operators_fk.go:1830`). No sibling path needed a matching edit (Hard-won Rule #2
   checked, not assumed). Domain CHECK constraints run through a genuinely separate
   path (`checkDomainConstraintsForRow`) and are out of this bucket's scope.

So the fix is six lines: `continue` when
`ci < len(tbl.NamedChecks) && tbl.NamedChecks[ci].NotEnforced`. Pinned by
`TestCheckConstraintNotEnforcedSkipsRuntimeEvaluation`
(`internal/executor/operators_fk_check_notenforced_skip_test.go`, FAIL pre / PASS
post), which asserts all three directions: a NOT ENFORCED check accepts violating rows
on INSERT *and* UPDATE; an enforced check still raises 23514 (the skip must not
over-fire); and a table carrying both kinds evaluates only the enforced one (the
index-alignment guard).

**Measured effect:** `scripts/pg-regress-runner.sh constraints` 1515 → **1465** lines
(hunks 30 → 31 — one surviving hunk split, not new divergence). The `NE_CHECK_TBL` and
`NE_INSERT_TBL_CON` hunks are gone. Unlike Bucket 1 this is a plain shrink, not an
unmasking.

**Deliberately not done:** `NOT ENFORCED` on **UNIQUE** constraints
(`UNIQUE_NOTEN_TBL`, `ALTER CONSTRAINT … ENFORCED`) still diverges — a different
constraint type on a different code path. Ledgered. Note that PG has **no** CHECK
`ALTER CONSTRAINT … ENFORCED` toggle at all (`tablecmds.c:12412`
`ATExecAlterConstrEnforceability` asserts `contype == CONSTRAINT_FOREIGN`), so none was
added here.

## 5. Bucket 3 — NOT NULL constraints resolvable by name (LANDED 2026-08-18)

**The sibling check the previous §5 demanded came back positive.** The DROP handler
`execAlterTableDropConstraint` (`internal/executor/operators_ddl.go:10719`) and the
RENAME path both ignored `tbl.NotNullConstraints`, so each raised
`42704 constraint "x" of relation "y" does not exist` for a constraint that plainly
exists. Two facts about the RENAME path are worth recording because both are traps:

1. It is **not** a function named `execAlterTableRenameConstraint` — it is an inline
   `case parser.AlterTableRenameConstraint:` at `:8796` inside `execAlterTable`.
2. It *does* reference `tbl.NotNullConstraints`, but only inside the
   `constraintNameInUse` collision helper (`:8836-8840`) — enough to make a
   grep-based check conclude "already handled". Its own resolution chain skipped
   NOT NULL entirely. This file is itself the evidence for Hard-won Rule #2: the
   arm carried a comment claiming it mirrored the drop path's "same four stores",
   and that comment was wrong in both the count and the contents.

**What landed.** A NOT NULL branch in each site, plus a shared
`clearNotNullConstraint` helper factored out of the existing
`case parser.AlterTableDropNotNull:` body (`:9659`) so DROP-by-name and
DROP-by-column apply the identical three steps (clear `Columns[i].NotNull`, splice
`NotNullConstraints`, catalog-heap resync). Matching is by constraint `Name` in the
new branch versus `ColName` in the old — that is the only difference. Two PG
refusals are honoured: the inherited-constraint guard (`InhCount > 0`, PG applies it
uniformly to all contypes at `tablecmds.c:14103-14107`) and the PK-membership
refusal `column "%s" is in a primary key` (`tablecmds.c:14154-14159`). Recursion to
children matches by **column name, not constraint name** — PG is explicit about the
asymmetry (`tablecmds.c:14251-14255`: "We search for not-null constraints by column
name, and others by constraint name"), so the NOT NULL cascade deliberately differs
from the CHECK cascade sitting directly above it. The rename branch mutates `Name`
directly rather than taking the `RenameIndex` arm, because PG's `conindid` is NULL
for a NOT NULL constraint and `rename_constraint_internal` therefore always takes
the `RenameConstraintById` branch (`tablecmds.c:4126-4131`).

**Measured effect:** `scripts/pg-regress-runner.sh constraints` 1465 → **1431**
lines, hunks 31 → **33**. Like Bucket 1 and unlike Bucket 2, this is an
**unmasking**, not a plain shrink: statements that previously aborted at the bogus
42704 now execute and expose gaps further down. Every added hunk was traced to a
pre-existing unrelated gap — the `ALTER CONSTRAINT … ENFORCED / NOT ENFORCED`
parser gap, the `ALTER CONSTRAINT … INHERIT / NO INHERIT` parser gap (Bucket 6), and
incomplete propagation of NOT NULL constraint rows into inheritance children
(affecting `VALIDATE CONSTRAINT` and `COMMENT ON CONSTRAINT`). No hunk this bucket
targeted reappeared. **Never compare a `constraints` number against anything
measured before 2026-08-18** — those predate the C19 harness fix.

**Deliberately not done:** the replica-identity-index membership refusal
(`tablecmds.c:14162-14167`) and the identity-column refusal (`:14174-14181`). Both
were already absent from the pre-existing `DROP NOT NULL <col>` path, so
implementing them here would have fixed a different bug than the one briefed.
Ledgered.

## 6. Bucket 6 — `ALTER CONSTRAINT … NOT VALID / [NO] INHERIT` (LANDED 2026-08-18)

**The bucket's own premise was wrong and the research pass corrected it.** Bucket 6
was sized as "`ALTER TABLE … ALTER CONSTRAINT` is a parse error". It is not:
`parseAlterConstraintAttrs` (`internal/parser/ddl.go:2925`) has handled
`[NOT] DEFERRABLE [INITIALLY …]` and `[NOT] ENFORCED` since an earlier slice, and
`execAlterTableAlterConstraint` (`internal/executor/operators_ddl.go:10996`) carries
real FK deferrability/enforceability logic — not an accept-and-ignore stub. Only two
spellings were genuinely broken, and they need *different* amounts of work:

1. **`NOT VALID` is parser-only.** PG rejects it **in the grammar action**, not the
   executor: `postgres/src/backend/parser/gram.y:2656-2685` special-cases
   `CAS_NOT_VALID` at `:2672-2676` with
   `ereport(ERROR, ERRCODE_FEATURE_NOT_SUPPORTED, "constraints cannot be altered to
   be NOT VALID")`. goopg reached the generic trailing-token error
   (`internal/parser/parser.go:169-189`) instead. Fixed at the `ALTER CONSTRAINT`
   dispatch site (`ddl.go:8892-8931`), which detects the `NOT VALID` that
   `parseAlterConstraintAttrs` deliberately leaves unconsumed (its comment at
   `:2916-2920`) and raises `0A000` with PG's exact text. **Verified byte-exact** —
   that statement's three output lines are now pure diff context.
2. **`[NO] INHERIT` needed both arms.** A grammar asymmetry drives the shape: bare
   `INHERIT` is its **own production** (`gram.y:2686-2699`) and is mutually exclusive
   with other attributes, while `NO INHERIT` arrives through
   `ConstraintAttributeSpec` (`gram.y:6249`). goopg mirrors that split — `NO INHERIT`
   inside `parseAlterConstraintAttrs`, bare `INHERIT` at the dispatch site — with new
   `AlterConstraintNoInherit` / `AlterConstraintHasInheritability` fields on
   `AlterTableAction` following the file's existing `Has*` tri-state convention.
   `catalog.NotNullConstraint.NoInherit` (`internal/catalog/catalog.go:244`) already
   existed, so this is a toggle path, not a data-model change. The executor arm
   (`execAlterConstraintInheritability`) mirrors
   `tablecmds.c:12615-12684 ATExecAlterConstrInheritability`: contype-gated to
   `CONSTRAINT_NOTNULL` (`tablecmds.c:12198-12334`), 42704 for an unknown name,
   **no-op when already in the requested state**, and propagation to children that is
   **one level, not recursive** — matched by column name, per Bucket 3's precedent.
   Durability rides `resyncNotNullCatalogHeap`, the Bucket 3 heap-resync pattern.

**Sibling check (Rule #2) came back negative this time**, and that is worth
recording because Bucket 3's came back positive: the third constraint-attribute
acceptor — `AlterTableAddNotNull`'s inline `NOT NULL <col> NOT VALID NO INHERIT`
form (`ddl.go:9859-9892`, exercised at `constraints.sql:831`) — is genuinely
independent code on a different dispatch branch with different AST fields, and
already wires `NoInherit` end to end. Traced, not grepped.

**Measured effect:** `scripts/pg-regress-runner.sh constraints` 1431 → **1411**
lines, hunks **33 → 33**. Neither a plain shrink nor an unmasking: both target
statements now behave, but every hunk containing them stays open on a *different*,
pre-existing defect. This is the bucket-interference signature and it is the main
lesson of this slice — **a bucket's measurable upside can be capped by an unrelated
bug sharing its hunk**:
- the `NOT VALID` hunk survives because the two lines above it
  (`ALTER CONSTRAINT unique_tbl_i_key ENFORCED / NOT ENFORCED`) diverge for a reason
  that has nothing to do with `ALTER CONSTRAINT` — `unique_tbl_i_key` never gets
  created, since `ADD CONSTRAINT … UNIQUE (i) DEFERRABLE INITIALLY DEFERRED` fails
  with `could not create unique index … Key (i)=(3) is duplicated`. goopg builds the
  index immediately instead of deferring the uniqueness check to commit — that is
  **Bucket 4's** defect, and it also invalidates the Bucket 2 ledger row's assumption
  that those two hunks were waiting on UNIQUE enforceability.
- both `[NO] INHERIT` hunks survive because every
  `EXECUTE get_nnconstraint_info(…)` in this file returns `(0 rows)` in goopg — a
  file-wide masking bug (ledgered) that hides the state assertions this bucket would
  otherwise have proved.

**Deliberately not done:** PG's non-topmost / partition-child `ALTER CONSTRAINT`
refusal (`tablecmds.c:12275-12317`) — goopg has no `ALTER CONSTRAINT`
partition-recursion story at all, so it is a milestone, not this slice. Ledgered
along with the `get_nnconstraint_info` masking bug and the newly surfaced
`would be inherited from more than once` divergence.

## 7. The `get_nnconstraint_info` `(0 rows)` masking bug — root cause pinned; `reg*[]` cast half LANDED (2026-08-18)

The file-wide masking bug named at the end of §6 was diagnosed to a **silent
wrong-answer bug in the cast evaluator**, not a catalog gap. The prepared
statement under test (`postgres/src/test/regress/sql/constraints.sql:816-820`) is

```sql
PREPARE get_nnconstraint_info(regclass[]) AS
SELECT conrelid::regclass AS tabname, conname, convalidated, conislocal, coninhcount
FROM pg_constraint WHERE conrelid = ANY($1)
ORDER BY conrelid::regclass::text COLLATE "C", conname;
```

A six-step bisect against a live server (research handoff
`m0134-0005-s06-nnconstraint-info-zero-rows`) **cleared** the obvious suspects:
goopg's `pg_constraint` does carry the `contype='n'` rows, and `convalidated` /
`conislocal` / `coninhcount` all match PG; `= ANY(...)` works; a plain `int[]`
prepared parameter binds fine; the `ORDER BY … COLLATE "C"` drops nothing.

**Root cause A (fixed here).** `evalCast` (`internal/executor/expr.go`) had **no
case for any reg\*-array type**, so `'{notnull_tbl1}'::regclass[]` fell through to
the function's terminal `return d, nil // pass-through for unknown types`. The array
literal stayed raw text — `pg_typeof` reported `text`, `(…)[1]::oid` raised 22P02 —
so every element comparison against an OID column silently evaluated **false**. No
error, no rows. PG instead resolves each element through `regclassin`
(`postgres/src/backend/utils/adt/regproc.c`) during `array_in`
(`postgres/src/backend/utils/adt/arrayfuncs.c`).

The fix adds one arm covering the whole family — `regclass[] regproc[]
regprocedure[] regtype[] regrole[] regcollation[] regnamespace[] regdictionary[]` —
shaped exactly like the existing `case "name[]":` precedent (`{…}` guard →
`parseTextArray` → per-element transform → `formatTextArray`), resolving each
element through **`regIdentifierInput`** (`internal/executor/reg_identifier.go`),
which is the same resolver the heap/encode twin already uses. A resolution miss now
raises the type's own undefined-object SQLSTATE (42P01 / 42704) instead of being
swallowed. The stale comment in the scalar `case "regclass":` arm claiming the
catalog is unreachable from `evalCast` predates that function's `ctx *Context`
parameter and is simply wrong.

One adjacent fix was forced: `evalExprSlot`'s pre-existing `TargetType ==
"regtype[]"` special case (the *reverse* oidvector→name direction, used by
`proargtypes::regtype[]`) treated **any** `KindString` as oidvector text via
`strings.Fields`, so it intercepted a brace-delimited literal before the new arm
could run. It now takes that branch only when the string does not begin with `{`.

**Rule #2 sibling check: NEGATIVE.** The storage/encode twin
`internal/executor/codec_array.go` (`encodeArrayElem`) already resolved reg\* array
elements through `regIdentifierInput` with an identical `-`/numeric-first contract;
`regnamespace`/`regdictionary` fall to its `default:` raw-text arm, which matches the
new cast arm's outcome (those two types still have no name-resolution seam anywhere —
a pre-existing documented exclusion, unchanged here).

**Root cause B (NOT fixed — the reason the hunks stay open).** With A fixed, the
*inline-literal* form works end to end (`conrelid = ANY('{notnull_tbl1}'::regclass[])`
returns the `nn` row), but the regress file's actual `EXECUTE
get_nnconstraint_info('{…}')` **still returns `(0 rows)`**. Reason:
`internal/postmaster/dispatch.go` uses a PREPARE's declared parameter types
(`prepDef.paramTypes`) only for an arity check and a coarse
`execParamTypeIncompatible` gate — **it never applies them as a coercion to the bound
value**. `PREPARE p(regclass) AS SELECT pg_typeof($1)` / `EXECUTE p('pg_class')`
reports `unknown`, so this is a *generic* PREPARE/EXECUTE gap, not a reg\*-specific
one, and it very likely mis-binds other types too (numeric/interval/date — unprobed).
Fixing A first is nonetheless the right order: coerced parameters have to land on a
working `reg*[]` cast to produce anything. Both halves are ledgered.

**Measured effect: none, and that is the expected result.**
`scripts/pg-regress-runner.sh constraints` 1411 → **1411** lines, hunks 33 → **33**,
and all 15 `get_nnconstraint_info` mentions still sit inside open hunks. Because the
regress file reaches the cast *only through* `EXECUTE`, root cause B gates the entire
measurable upside of fixing A. This is a third distinct outcome shape for this case,
alongside Bucket 1's *unmasking* (diff grew) and Bucket 6's *bucket interference*
(hunks held open by an unrelated defect): a **stacked root cause**, where the fix is
correct, verified, and a strict prerequisite, but invisible to the metric until the
layer above it is also fixed. Do not read the flat number as "the slice did nothing"
— read it as "B is next".

**Also recorded, not fixed:** `SELECT '{notnull_tbl1}'::regclass[]` renders
`{16406}` where PG renders `{notnull_tbl1}` (PG models regclass as an OID with a
name-rendering *output* function). Matching that needs a new Datum kind or a
wire-formatter change; the comparison semantics this slice was after do not depend
on it.

## 8. Root cause B — declared-parameter-type coercion at `EXECUTE` (LANDED 2026-08-18)

§7's stacked root cause B is now fixed, and the metric moved: `constraints`
**1411 → 1376 lines** (−35), hunks 33 → **34** (+1 — a fixed region split one open
hunk in two, the same *unmasking* shape Bucket 1 produced), `get_nnconstraint_info`
mentions 15 → **14**, and the remaining 14 are unified-diff *context* lines (the
literal `PREPARE`/`EXECUTE` statement text, byte-identical on both sides) rather
than divergences of their own — what still differs is the surrounding result-row /
NOTICE / ERROR text. This is the empirical confirmation of §7's claim that A was a
correct, strictly-prerequisite fix whose upside was gated by B.

**What PG does.** `postgres/src/backend/commands/prepare.c:EvaluateParams` runs
`coerce_to_target_type(..., COERCION_ASSIGNMENT, COERCE_IMPLICIT_CAST, -1)` over
every supplied argument against `pstmt->argtypes[i]` before execution. goopg had
only the *validation* half of that (arity check + `execParamTypeIncompatible`,
which emits PG's exact 42804 wording) and then bound the raw literal datum — so a
`regclass[]` argument stayed `KindString` and every OID comparison in the plan
evaluated false, silently, with no error.

**What landed.** A thin exported wrapper `executor.CoerceParamToDeclaredType`
(`internal/executor/expr.go`, beside `evalCast`) normalises the declared spelling
(quotes stripped, lower-cased, typmod parenthesis dropped, trailing `[]` kept) and
delegates to `evalCast`. Both dispatch sites call it (Hard-won Rule #2 — they are a
sibling pair and were both blind): the `*parser.ExecuteStmt` branch and the
`CREATE TABLE … AS EXECUTE` branch of
`internal/postmaster/dispatch.go:dispatchSimpleQueryViaExecutor`. Cast failures
surface with the cast's own SQLSTATE (42P01 for an unresolvable relation name)
instead of a silent wrong answer.

**Design deviation worth remembering.** Scalar (non-array) `reg*` targets are
resolved *inside the wrapper* via `regIdentifierInput`, **not** by widening
`evalCast`'s own `regclass` case. `evalCast`'s `case "regclass"` is deliberately a
`KindInt`-only pass-through: real scalar `'name'::regclass` resolution lives inline
in `evalExprSlot`'s `*optimizer.CastExpr` arm and in `evalFuncCall`'s reg\* arm,
neither of which a bound EXECUTE parameter ever traverses (the parameter value never
becomes a CastExpr/FuncCall AST node). Widening `evalCast` was tried and reverted —
it regressed `TestRegCastToStringRendersName`, whose fixture deliberately makes
system tables un-resolvable by name and pins `'pg_type'::regclass::text` to the old
silent pass-through. Keeping the resolution in the wrapper leaves every other
`evalCast` caller byte-identical.

**Still open after this slice** (both ledgered):
- `pg_typeof($1)` reports `unknown` for **every** bound EXECUTE parameter, of any
  type. `internal/optimizer/planner.go`'s `resolveExpr`/`resolveExprAfterAggregate`
  fold `pg_typeof(<arg>)` to a `StringConst` at *plan* time via
  `pgTypeofDisplayName(exprType(arg))`; `exprType` has no `*ParamRef` case and
  `optimizer.ParamRef` carries no `Type` field, so it falls through to `unknown`,
  and `evalFuncCall`'s runtime `pg_typeof` case is dead code for `$N`. This is a
  static-typing/display gap, not a value-correctness gap — the coercion above was
  verified with `WHERE oid = $1` probes instead.
- `regnamespace`/`regdictionary`/`regoper`/`regoperator`/`regconfig` parameters
  still do not resolve (no name-resolution seam), matching the reg\*[] array arm's
  own caveat at `expr.go:3687-3689`.

## 9. Next slice for this case

**Bucket 4 is now the highest-value target for this file**: its deferred-UNIQUE
defect is what caps Bucket 6's *and* (contrary to that row's stated assumption)
Bucket 2's remaining hunks. It is still milestone-sized — statement-level/deferred
UNIQUE checking is a real executor feature — so it needs a research pass and its own
decomposition, not a direct brief.

After those: Bucket 3's leftover NOT NULL inheritance-propagation gap. Bucket 5
(GiST `circle_ops`) is a milestone. **Bucket 7 still has no pinned root cause — do
not brief from it.**

## 10. Bucket 4 — the missing statement-end tier (core fix LANDED 2026-08-18)

### 10.1 Bucket 4 was mis-sized as a milestone; it was one slice

The §2 Bucket 4 row called this "real constraint-timing/MVCC work" and a
milestone. The research pass (`tmp/ralph-handoffs/m0134-0005-b4-research/`)
**refuted that**: the milestone-sized machinery already existed. M0119-0004 landed
`internal/executor/deferred_unique.go` — a working deferred-to-COMMIT queue with a
drain at commit, a `SET CONSTRAINTS` resolver, and an NND sibling — *before* M0134-0005
started. Nothing new had to be built.

**goopg had two tiers where PG has three.**

| constraint state | per-row behavior | drain point |
|---|---|---|
| NOT deferrable | synchronous check | — (unchanged) |
| `DEFERRABLE` (INITIALLY IMMEDIATE, or `SET CONSTRAINTS … IMMEDIATE`) | **queue, never block** | **end of statement** ← was MISSING |
| deferred to COMMIT (`INITIALLY DEFERRED` / `SET CONSTRAINTS … DEFERRED`) | queue, never block | COMMIT (already worked) |

PG applies the middle tier to **every** deferrable index unconditionally:
`postgres/src/backend/catalog/index.c:2080-2082` sets `pg_index.indimmediate = false`
whenever `deferrable`, *independent of the INITIALLY mode*; only the recheck **timing**
differs. The per-row insert then takes `UNIQUE_CHECK_PARTIAL` in
`execIndexing.c:ExecInsertIndexTuples`, which never errors and instead flags a recheck
that the after-trigger queue fires (`unique_key_recheck`).

goopg's `uniqueCheckDeferred` collapsed the two deferrable tiers into one boolean
gated on "deferred to commit" **and** on `InExplicitTransaction()`. So plain
`DEFERRABLE` still blocked synchronously on the first transient duplicate — the
canonical `UPDATE unique_tbl SET i = i + 1;` failed even inside an explicit `BEGIN`.

### 10.2 What landed

- `internal/executor/deferred_unique.go` — `uniqueCheckDeferred` now answers only
  "needs a partial (queue, never block) check?" = `idx != nil && idx.Deferrable`; the
  `InExplicitTransaction()` gate is **removed** (PG does not have it). The old resolver
  body moved to a new companion `uniqueCheckDeferToCommit`, so the two questions no
  longer share one predicate. `queueDeferredUniqueCheck` **and** its NND twin
  `queueDeferredNNDUniqueCheck` (Rule #2) stamp a `DeferToCommit` tier tag at enqueue
  time. New `RunStmtEndDeferredUniqueChecks` mirrors `RunDeferredUniqueChecks`, draining
  only the `DeferToCommit == false` subset.
- `internal/executor/session.go` — `DeferredUniqueCheck.DeferToCommit`; new
  `TakeDeferredUniqueChecksStmtEnd()`. `constraintDeferredByName` and the
  `FKConstraintDeferred`/`UniqueConstraintDeferred`/`ExclusionConstraintDeferred`
  delegates are **unchanged** — deliberately, so the FK tier keeps its semantics. The
  COMMIT-path `TakeDeferredUniqueChecks()` still takes everything, as a safety net.
- `internal/postmaster/dispatch.go` — statement-end drain in the simple-query loop.
- `internal/postmaster/dispatch_extended.go` — **the Rule #2 twin, and it was NOT
  symmetric.** The extended-protocol out-of-block `Execute` path commits via
  `ectx.CommitTransaction` (`internal/executor/context.go:1030`), a bare
  `TxnMgr.Commit` that runs **no** deferred-check drain at all; only the simple-query
  `TxCommit` verb and `transactionOp.execCommit` do. The drain was added there
  explicitly rather than assumed covered.
- `internal/testport/deferred_unique_stmt_end_e2e_test.go` — 5 tests; the 3 new
  behavioral ones are FAIL-pre (`23505` on `unique_tbl_i_key`) / PASS-post.

The DML call sites needed **zero** changes: `checkUniqueIndexesForInsert` /
`checkUniqueIndexesForUpdate` already fan into the shared queue pair.

### 10.3 Measured payoff, and the honest reading of it

1376 → **1299** lines (−77); hunks 34 → **36** (+2). Hunk count rose because
unmasking splits context runs — the same shape Bucket 1 produced. **Do not compare to
any pre-2026-08-18 number** (pre-C19 harness).

Two results that look alarming in the raw diff and are **not** what they appear:

- *"4 duplicate rows persist, no error"* at the `INSERT (3,'Three')` block (9 rows vs
  expected 5) is a **cascade of `unique_tbl_i_key` never being created**, not a
  data-integrity regression. With no constraint in place, those duplicates are
  legitimately unconstrained. Same for the downstream `ALTER CONSTRAINT … ENFORCED`
  hunks now reading `constraint … does not exist`.
- The `ADD CONSTRAINT … UNIQUE (i) DEFERRABLE INITIALLY DEFERRED` failure **changed
  shape**, from `Key (i)=(3) is duplicated` to `Key (i)=(1) is duplicated`, while the
  immediately preceding `SELECT * FROM unique_tbl` matches PG byte-for-byte. §7's
  prediction that this statement would need no code change once the enqueue gate was
  fixed is therefore **refuted**.

### 10.4 The newly EXPOSED defect — DDL bulk scans got tuple liveness BACKWARDS (root cause CONFIRMED and FIXED 2026-08-18, M0134-0005c)

**§10.4's original hypothesis — "the build scan counts dead row versions" — is
REFUTED.** A plain committed `UPDATE` leaving superseded versions behind does *not*
provoke a spurious duplicate. Probed directly on a live server: `UPDATE p SET j=j+100`
followed by `ALTER TABLE p ADD CONSTRAINT p_i_key UNIQUE (i)` (and the
`CREATE UNIQUE INDEX` spelling) both succeed. Dead versions were never the trigger.

The real trigger is the **`BEGIN; UPDATE …; ROLLBACK;` block that precedes** the
deferred-constraint block in `constraints.sql` — a statement that fails on a genuine
duplicate and rolls back. Minimal confirmed reproducer:

```sql
CREATE TABLE ut4 (i int UNIQUE DEFERRABLE, t text);
INSERT INTO ut4 VALUES (0,'one'),(1,'two'),(2,'tree'),(3,'four'),(4,'five');
BEGIN; UPDATE ut4 SET i = 1 WHERE i = 0;   -- fails (real dup)
ROLLBACK;
UPDATE ut4 SET i = i + 1;                  -- succeeds; SELECT * correctly shows 1..5
ALTER TABLE ut4 DROP CONSTRAINT ut4_i_key;
ALTER TABLE ut4 ADD CONSTRAINT ut4_i_key UNIQUE (i) DEFERRABLE INITIALLY DEFERRED;
-- goopg (pre-fix): ERROR could not create unique index "ut4_i_key" / Key (i)=(1) is duplicated
-- PG: succeeds
```

**Root cause.** Three DDL bulk-scan functions in `internal/executor/operators_ddl.go`
decided liveness by a purely *structural* test — `Xmin == Invalid ⇒ dead`,
`Xmax != Invalid ⇒ dead` — that never consulted transaction commit/abort status:

| function | line | scan |
|---|---|---|
| `collectBTreeEntries` | ~11890 | index-build key collector |
| `forEachLiveRow` | ~11261 | generic DDL row iterator |
| `validateFKConstraintExistingRows` | ~11396 | FK validation scan |

After the aborted UPDATE the page carries two poisoned tuples, and the structural test
gets liveness wrong **in both directions**: the real `i=0` row now bears a non-invalid
but *aborted* `xmax` and is wrongly dropped, while the aborted UPDATE's phantom `i=1`
version bears an *aborted* `xmin` and is wrongly kept. That phantom then collides with
the legitimate `i=1` the later `UPDATE i=i+1` produces. Nothing prunes it, so the
poisoning is permanent for that page — every subsequent `CREATE INDEX` /
`ADD CONSTRAINT` / FK-validate on the table inherits it.

**PG oracle.** `postgres/src/backend/access/heap/heapam_visibility.c:1205`
`HeapTupleSatisfiesVacuumHorizon`: an xmin that is neither committed, in-progress, nor
current is "aborted or crashed" ⇒ `HEAPTUPLE_DEAD` (~:1291-1294). That verdict feeds
`postgres/src/backend/access/heap/heapam_handler.c:1415-1613`
`heapam_index_build_range_scan`'s `indexIt`/`tupleIsAlive` switch — the structural
analogue of goopg's scan loop.

**Fix (landed 2026-08-18).** All three sites now call the pre-existing abort-aware
helper `isLiveForUniqueCheck(ctx, xmin, xmax)`
(`internal/executor/operators_storage.go:8767-8832`), which already consults
`ctx.Snap.HasAborted` / `ctx.TxnMgr.HasAbortedXID` and was already used at 6+
INSERT/UPDATE-time call sites — it was simply never wired into the DDL scans. No new
liveness predicate was written; all three call sites already had `o.ctx` in scope, so
no parameter threading was needed.

**On Rule #2 (sibling paths).** `ADD CONSTRAINT … UNIQUE` and `CREATE UNIQUE INDEX`
are **not** two paths here — both route through `bulkBuildBTreeFull` →
`collectBTreeEntries`, so the one edit covers the pair. This was measured, not
assumed: `TestPort_CreateUniqueIndexSkipsAbortedXminPhantom` exercises the
`CREATE UNIQUE INDEX` spelling of the same sequence independently. `backfillBTree`
(~:12040) carries the identical naive check but has **zero callers** — deliberately
left untouched as dead code (ledgered).

**Guards** (`internal/testport/ddl_scan_abort_liveness_test.go`, all three FAIL-pre /
PASS-post, proven by stashing only `operators_ddl.go`):
`TestPort_AddConstraintUniqueSkipsAbortedXminPhantom` (the `ut4` sequence),
`TestPort_CreateUniqueIndexSkipsAbortedXminPhantom` (the twin spelling), and
`TestPort_AddConstraintUniqueCatchesLiveRowWithAbortedXmax` — the **inverse**
direction, where a rolled-back `DELETE`'s aborted xmax must not hide a live duplicate
and the constraint addition must still correctly ERROR. The inverse case matters
because the pre-fix behaviour there was a *silent false success*, not an error: a
liveness bug that over-reports duplicates is loud, but the same bug under-reporting
them lets a violating table acquire a UNIQUE constraint it does not satisfy.

**The generalisable lesson.** The naive test is not merely *imprecise*, it is
*inverted* under abort. Any structural "is this tuple live" check written without an
abort consult will pass every test whose fixtures only ever commit — which is why this
survived until a regress file happened to roll a statement back mid-sequence.

### 10.5 Measured payoff of 10.4, and remaining Bucket 4 work

**Measured 2026-08-18, after the liveness fix:** `constraints` 1299 → **1251** lines
(−48), hunks 36 → **35**. Command: `timeout 300 scripts/pg-regress-runner.sh --verbose
constraints`; artifact `tmp/regress-diffs/constraints.diff`. **Never compare against a
pre-2026-08-18 number** — the C19 harness fix moved the baseline.

The decisive check is not the line count but the disappearance of the error class:
`grep -n "is duplicated\|could not create unique index" tmp/regress-diffs/constraints.diff`
now returns **nothing**. `ALTER TABLE unique_tbl ADD CONSTRAINT unique_tbl_i_key UNIQUE
(i) DEFERRABLE INITIALLY DEFERRED` succeeds, which also retires the downstream cascade
(§10.3) of hunks that only diverged because that constraint never existed.

**Bucket 4 is now closed as a line driver.** With the cap removed, the 35 surviving
hunks are dominated by work outside it: CHECK-constraint inheritance naming, COPY FROM
not rejecting bad rows, the GiST `circle` block (Bucket 5 — a genuine milestone), and
the NOT NULL inheritance block (Bucket 3's leftover). Sizing the next slice off this
file should start from that classification, not from the residual line count.

Still open, all independent of check timing:

- Partitioned `parted_uniq_tbl` diverges two ways: goopg materializes **1**
  `pg_constraint` row where PG has **3** (no per-partition unique-constraint catalog
  rows), and the deferred-violation case enforces correctly but emits the
  ERROR/`COMMIT` echo in the opposite order. Ledgered separately.
- Research slices 3 and 4 — the `UNIQUE ENFORCED` / `NOT ENFORCED` grammar rejection,
  and the `ALTER CONSTRAINT … ENFORCED` contype gate. Slice 4 was blocked on §10.4 for
  end-to-end testability; **that block is now lifted**, so both are selectable small
  independent slices.

## 11. Slice `d` — `[NOT] ENFORCED` clause fidelity (LANDED 2026-08-18, M0134-0005d)

This is §10.5's "research slices 3 and 4", landed together as one implementer slice
(two line-disjoint parts, one file each). Both were pinned by an empirical probe pass
before any brief was written — the third consecutive time on this case that a probe
was the deciding evidence.

### 11.1 Part A — the grammar must ACCEPT `[NOT] ENFORCED`, then reject it semantically

PG does not reject `CREATE TABLE t(i int UNIQUE ENFORCED)` in the grammar. `ENFORCED`
is an ordinary `ConstraintAttributeElem` (`gram.y:4135-4150`, `n->location = @1`); the
rejection happens later in `parse_utilcmd.c:3991-4021` `transformConstraintAttrs`,
cases `CONSTR_ATTR_ENFORCED` / `CONSTR_ATTR_NOT_ENFORCED`, which permit the attribute
only on CHECK and FOREIGN KEY and raise, for UNIQUE / PRIMARY KEY / EXCLUSION:

```
ERROR:  misplaced ENFORCED clause          -- caret under ENFORCED
ERROR:  misplaced NOT ENFORCED clause      -- caret under NOT (start of the pair)
```

both `errcode(ERRCODE_SYNTAX_ERROR)` = **42601**, both **with** `parser_errposition`.
Caret columns verified by counting against
`postgres/src/test/regress/expected/constraints.out:740-746`, not assumed.

goopg's failure was a token-stream accident, not a missing feature: the four
column-level constraint sites call `parseConstraintDeferrable`
(`internal/parser/ddl.go:2786`) as their only trailer parser, and it unconditionally
swallows a leading `NOT` while probing for `NOT DEFERRABLE`. So `NOT ENFORCED` lost
its `NOT` and left `ENFORCED` dangling (`syntax error at or near "expected ',' or ')'
(got enforced)"`), while bare `ENFORCED` was never inspected at all.

Fix: new `rejectMisplacedEnforced` (`ddl.go`, immediately after
`rejectDuplicateEnforced`) reuses the pre-existing `isEnforcedAttr` (`ddl.go:2818`,
which deliberately requires an `ENFORCED` peek so it never matches `NOT NULL` /
`NOT VALID` / `NOT DEFERRABLE`) and the pre-existing `SyntaxError{Pos, Message,
Raw: true}` template — `SyntaxError`'s default SQLSTATE is already 42601, and `Raw`
suppresses the `syntax error at or near` wrapper. It is called **before**
`parseConstraintDeferrable` at all four column-level sites: bare column `PRIMARY KEY`,
bare column `UNIQUE`, and the two named-inline `CONSTRAINT <c> PRIMARY KEY | UNIQUE`
spellings. Ordering is the whole fix — after the call, the `NOT` is already gone.

**Deliberately not table-level.** `UNIQUE (i) ENFORCED` is a *different* PG code path
(`processCASbits`, `gram.y:19447-19543`) with a different message and SQLSTATE —
`"UNIQUE constraints cannot be marked ENFORCED"`, **0A000** — so reusing the
column-level message there would have been a new divergence, not a fix. No
`constraints.sql` line exercises it; ledgered 2026-08-18.

### 11.2 Part B — `ALTER CONSTRAINT … [NOT] ENFORCED` carried a position PG does not

Message and SQLSTATE (42809) already matched PG byte-for-byte; the sole divergence was
a spurious `LINE 1: …` + caret, because two `ExecError` literals in
`execAlterTableAlterConstraint` (`internal/executor/operators_ddl.go` ~:11061, ~:11091)
set `Pos: act.Pos()`. PG's `tablecmds.c:12254-12258` `ereport` has no
`parser_errposition`. Dropping the field is sufficient: `Pos == 0` suppresses the wire
`'P'` field (convention asserted by `internal/postmaster/plan_error_position_test.go:12-16`).

Four sibling `ExecError` sites in the same function carry the same `Pos: act.Pos()`,
and the researcher flagged them as the same suspected bug — but they were left alone
**on purpose**: at least one of them (`constraints cannot be altered to be NOT VALID`)
currently matches PG *with* a LINE+caret in the expected output, so a blanket sweep
would regress it. Any future fix there is per-site verification against
`constraints.out`, not a sweep. Ledgered 2026-08-18.

### 11.3 Measured

`constraints` 1251 → **1232** lines (−19); hunks **35 → 35** (no split this time). The
decisive check is again the disappearance of a class, not the count:
`grep -n ENFORCED tmp/regress-diffs/constraints.diff` now returns only three
*context* lines — every `+`/`-` ENFORCED line is gone, including the `NOT ENFORCED`
pair and both `ALTER CONSTRAINT` LINE/caret additions. Command: `timeout 300
scripts/pg-regress-runner.sh --verbose constraints`. **Never compare against a
pre-2026-08-18 number.**

Guards: `TestParseColumnConstraintMisplacedEnforced` (8 cases — bare/`NOT` ×
`UNIQUE`/`PRIMARY KEY` × bare/named-inline, asserting message, `Raw`, and the exact
caret column, plus a `DEFERRABLE`/`NOT DEFERRABLE`/`INITIALLY DEFERRED` regression
block for the trailer path Part A edits) in `internal/parser/ddl_test.go`; a `Pos == 0`
assertion added to `TestFKAlterConstraint/NonFKConstraintRejected`
(`internal/executor/operators_fk_alter_constraint_test.go`); and
`internal/postmaster/exec_error_position_test.go` guarding the wire-conversion layer.
Both production-code fixes were FAIL-pre/PASS-post demonstrated by stashing the single
changed file.

### 11.4 What is left on this file

Unchanged from §10.5 minus the two now-landed research slices: CHECK-constraint
inheritance naming, COPY FROM not rejecting bad rows, the NOT NULL inheritance
leftover (Bucket 3), the partitioned `parted_uniq_tbl` pair of divergences, and Bucket
5 (GiST `circle_ops`) which is a genuine milestone. **Bucket 7 still has no pinned root
cause — do not brief from it.**

## 12. Slice `e` — the COMMIT-tier deferred unique recheck is blind to HOT chains (M0134-0005e)

Status: **draft → accepted** (root cause pinned by live probe 2026-08-18, both
engines; see §12.2). This is the *silent* integrity gap ledgered by slice `c`:
unlike every earlier bucket in this document, the divergence is not a wrong
message — goopg **commits a transaction PostgreSQL rejects**, leaving a genuine
duplicate on disk.

### 12.1 The SQL surface

`postgres/src/test/regress/sql/constraints.sql:510-517` (the `unique_tbl` block
whose own comment reads *"test a HOT update that invalidates the conflicting
tuple. the trigger should still fire and catch the violation"*). Reduced to a
minimal probe:

```sql
CREATE TABLE t (i int, t text);
INSERT INTO t VALUES (3, 'three');
ALTER TABLE t ADD CONSTRAINT t_i_key UNIQUE (i) DEFERRABLE INITIALLY DEFERRED;
BEGIN;
  INSERT INTO t VALUES (3, 'Three');                       -- queues a deferred check
  UPDATE t SET t = 'THREE' WHERE i = 3 AND t = 'Three';    -- non-key column ⇒ HOT
COMMIT;
```

PG 18.3: `COMMIT` raises `23505 duplicate key value violates unique constraint
"t_i_key"` and the transaction rolls back (1 row remains). goopg HEAD: `COMMIT`
succeeds, leaving **2 rows with `i = 3`**.

### 12.2 Root cause — a false premise, stated exactly

The premise *"`recheckDeferredUniqueKey`'s per-pointer liveness fetch resolves
the current live version of the candidate row"* is **false**.

`recheckDeferredUniqueKey` (`internal/executor/deferred_unique.go:239-268`)
scans the b-tree with `tree.RangeScan(key, key, …)` and, for each returned
`ItemPointer`, fetches the tuple **at exactly that pointer** via
`storage.PageGetHeapTuple(slot.Page(), ptr.Offset)`, then judges it with
`isLiveForUniqueCheck`. It never follows `t_ctid`.

The `UPDATE` touches only a non-key column, so it takes the HOT path
(`hotUpdateEligible` → `tryApplyHOTUpdate`,
`internal/executor/operators_storage.go:4609-4668, 3896-4136`) which — correctly,
matching PG — performs **no index maintenance at all**. It stamps the old slot's
`xmax` to the updater's own XID and its `t_ctid` to the new HEAP_ONLY_TUPLE slot.
So the candidate's only b-tree entry is the INSERT-time one, now pointing at a
slot that `isLiveForUniqueCheck` *correctly* calls dead (`xmax == selfXID`,
`operators_storage.go:8805-8808`). The live successor is one `t_ctid` hop away
and is never visited. Live count comes out 1 instead of 2; the check passes.

Everything else in the chain is already right, and was verified rather than
assumed: the UPDATE is deliberately **not** re-enqueued
(`indexKeyColumnsChanged` gate, `operators_storage.go:8312` — PG does the same),
and the COMMIT-tier drain **does** run on both commit paths
(`internal/postmaster/txn_verb.go:333`; `internal/executor/operators_tx.go:140`).

PG oracle: `postgres/src/backend/commands/trigger.c` `unique_key_recheck` exists
precisely to re-fetch a queued candidate through its update chain before judging
it; goopg's recheck was never given that capability.

### 12.3 Not a Rule #2 divergence — but check the twin anyway

Both protocols reach the *same* shared drain: an explicit `COMMIT` over the
extended protocol parses to the same `optimizer.TxCommit` node and dispatches
through `txn_verb.go`'s shared verb handler. The defect therefore lives one layer
**below** M0134-0005b's asymmetry fix (bare `ectx.CommitTransaction` running no
drain), and a single fix covers both. `recheckDeferredNNDUniqueKey` (same file)
is heap-scan based, resolves no stored index pointers, and is unaffected.

### 12.4 Fix shape

In `recheckDeferredUniqueKey`'s `RangeScan` callback, resolve each pointer to its
chain **tail** before the liveness test, reusing the pre-existing primitives
rather than inventing a predicate: `isChainTailCTID`
(`operators_storage.go:653-670`) to detect the tail, `maxCTIDChainWalk`
(`operators_storage.go:499-509`) as the loop bound, and the traversal shape of
`epqFollowChainFull` (`operators_storage.go:674-733`) minus its
predicate/snapshot evaluation — the recheck needs only `isLiveForUniqueCheck` on
the tail's Xmin/Xmax, not full MVCC visibility. De-duplicate `seen` by the
**resolved tail** pointer, not the raw b-tree pointer, so two entries whose
chains converge on one physical tuple are not double-counted.

### 12.5 Known adjacent exposure (ledgered, not fixed here)

The *immediate* (non-deferred) probes `uniqueCheckWithWait`
(`operators_storage.go:8573`) and `exclusionCheckOnce`
(`operators_storage.go:8512`) use the identical direct-pointer-fetch pattern with
no chain-follow. They are far less exposed — each runs synchronously right after
its own statement's index maintenance — but a same-transaction HOT update of an
earlier statement's row could in principle open the same blind spot. Not probed.

## 13. Slice `f` — per-partition index clones dropped the parent's constraint attributes (M0134-0005f)

**Status: LANDED 2026-08-18.** Measured `constraints` **1181 → 1164** lines (−17),
hunks **33 → 33**.

### 13.1 The divergence (two symptoms, one cause)

Upstream case: `postgres/src/test/regress/sql/constraints.sql:432-445`
(`parted_uniq_tbl`). Pinned by a live two-engine probe before briefing, not guessed:

1. **`pg_constraint` fanout.** PG lists three rows —
   `parted_uniq_tbl_i_key`, `parted_uniq_tbl_1_i_key`, `parted_uniq_tbl_2_i_key`.
   goopg listed only the parent's.
2. **Deferred enforcement.** With the constraint deferred, PG accepts the duplicate
   INSERT and raises `23505` at **COMMIT**. goopg raised at the INSERT.

### 13.2 Root cause

Both goopg sites that build a partition's copy of the parent's UNIQUE/PK index
cloned only the *shape* fields and silently dropped the *constraint* fields:

- `internal/executor/operators_ddl.go:4611` — `CREATE TABLE … PARTITION OF`
- `internal/executor/operators_ddl.go:8380` — `ALTER TABLE … ATTACH PARTITION`

Each called `createBTreeIndex(…, parentIdx.Unique, parentIdx.Primary,
parentIdx.NullsNotDistinct, parentIdx.ColDescending, parentIdx.ColNullsFirst)` and
never forwarded `Deferrable`, `InitiallyDeferred`, or `IsConstraint`. The two
downstream consumers were already correct and are what proved the fix:
`uniqueCheckDeferred` (`internal/executor/deferred_unique.go:51-53`) reads
`idx.Deferrable`; `PGConstraintRowsForDBOid` (`internal/catalog/catalog.go:6624-6627`)
filters on `idx.IsConstraint`. Neither was touched.

**Why PG cannot have this bug.** `postgres/src/backend/commands/indexcmds.c:DefineIndex`
recurses on the **same `IndexStmt*`** for each partition (dispatch at
`indexcmds.c:706` for `RELKIND_PARTITIONED_TABLE`), so
`deferrable`/`initdeferred`/`isconstraint` propagate for free. PG has no per-field
copy step to get wrong; goopg's clone-a-new-`catalog.Index` architecture does. This
is an *architectural* divergence class, not a typo — expect the same shape wherever
goopg clones a catalog object rather than re-running its constructor.

### 13.3 The fix

The three fields were added to the pre-existing `btreeIndexProps` optional-arg struct
(`operators_ddl.go:~11624`) and applied inside `createBTreeIndex`'s existing
`if xp != nil` block (`~:11751`), alongside Fillfactor/DeduplicateItems/Tablespace —
rather than growing the positional signature. Both clone sites forward identically.

Rule #2: the two sites **are** a genuine twin pair (confirmed by reading both, not
grepping). The column-level declared-UNIQUE path on a partition (`:4620`) and the
plain-index inheritance loop (`:4631-4674`) are **not** affected — no parent
constraint to forward, or explicitly excluded.

### 13.4 What this slice did NOT fix — the named `SET CONSTRAINTS` gap

The probe's literal SQL uses `SET CONSTRAINTS parted_uniq_tbl_i_key DEFERRED` — the
**named** form. That still diverges, and it is a *second, distinct* bug the
implementer correctly escalated rather than absorbing:

`BasicSession.SetConstraintsNamed` (`internal/executor/session.go:383`) populates a
flat `map[string]bool` keyed by the name the user typed — the **parent's**
(`parted_uniq_tbl_i_key`). The per-partition clone is auto-named
`parted_uniq_tbl_1_i_key`. At recheck time `uniqueCheckDeferToCommit`
(`internal/executor/deferred_unique.go:66`) looks up `sess.UniqueConstraintDeferred(idx.Name, …)`
with the **child's** name, misses, and silently falls back to `idx.InitiallyDeferred`
(false) — so the check runs at statement end.

PG resolves this in `postgres/src/backend/commands/trigger.c:AlterConstraint`, which
resolves the named constraint's OID and then **walks `pg_constraint.conparentid` to
collect every descendant partition-child constraint OID** before setting deferred
state. goopg has no `conparentid` equivalent: `catalog.Index` carries no
"cloned from constraint X" linkage, and `session.constraintDeferral` is partition-blind.

Consequence for this doc's measurement: the surviving `parted_uniq` hunk
(`@@ -590,13 +579,13 @@`, 5 matches) is **this gap**, not a psql transcript-echo
artifact — the ERROR line precedes `COMMIT;` in goopg's transcript precisely because
goopg errors at the INSERT. Filed as **M0134-0005g** with a ledger row. The same
flat resolver serves `FKConstraintDeferred`, so FK constraints inherited onto
partitions are very likely affected too — unprobed.

### 13.5 Guards

`internal/testport/partitioned_deferrable_unique_test.go` (new, 3 tests):

- `TestPort_PartitionedDeferrableUniqueDefersToCommitViaSetConstraintsAll` — the
  COMMIT-tier `23505` via the **unnamed** `SET CONSTRAINTS ALL DEFERRED` form (the
  form this slice actually fixes). Proven FAIL-pre by stashing **only**
  `operators_ddl.go`: pre-fix it fails at the INSERT with
  `duplicate key value violates unique constraint "parted_uniq_tbl_1_i_key"`. Not vacuous.
- `TestPort_PartitionedUniqueConstraintFansOutToPgConstraint` — the 3-row
  `pg_constraint` result for **both** creation paths; the `ATTACH PARTITION` half is
  what proves the twin site.
- `TestPort_PartitionedNonDeferrableUniqueErrorsAtInsert` — **negative** guard: a
  non-deferrable partitioned UNIQUE must still error at INSERT time. Continuing the
  0005e lesson, one-directional guards are not sufficient for tier-selection fixes.

## 14. M0134-0005g — the named `SET CONSTRAINTS` form (LANDED 2026-08-18)

§13.4 filed this as "needs a constraint-hierarchy linkage that does not exist anywhere
in goopg today; strictly larger than one slice". **That sizing was wrong**, and a
read-only probe refuted it before any code was written — the linkage already exists.

### 14.1 What the probe refuted

`catalog.Index.PartitionParentOID` (`internal/catalog/catalog.go:1818`) has existed all
along, together with a parallel `InMemory.indexPartitionChildren` map (`:2369`) and its
`RegisterIndexPartitionChild` / `IndexPartitionChildren` accessors (`:4744` / `:4752`).
Two goopg sites already populate it correctly (`internal/executor/operators_ddl.go:6846`
and `:8610`). The **two 0005f clone sites did not** — they call `createBTreeIndex` and
never assign the field, so the child index had no route back to its parent. No new
`catalog.Index` field was required; no `conparentid` catalog column was required.

The lesson generalises past this slice: §13.4's sizing came from reading the *consumer*
(`uniqueCheckDeferToCommit`) and the PG oracle, and inferring the producer side must be
missing. It was not — it was merely unwired at two of four sites. **Probe the producer
before sizing a "needs new infrastructure" claim.**

### 14.2 Root cause and fix

`BasicSession.SetConstraintsNamed` (`internal/executor/session.go:383`) keys a flat
`map[string]bool` by the name the user typed — the parent's, `parted_uniq_tbl_i_key`.
goopg auto-names the per-partition clone `parted_uniq_tbl_1_i_key`, so
`uniqueCheckDeferToCommit` (`internal/executor/deferred_unique.go:66`) looked up the
**child's** name, missed, and silently fell back to `idx.InitiallyDeferred` (false) —
statement-end enforcement where PG defers to COMMIT.

Fix, two parts:

1. `internal/executor/operators_ddl.go` — both clone sites (`~:4615` `CREATE TABLE …
   PARTITION OF`, `~:8402` `ALTER TABLE … ATTACH PARTITION`) now set
   `childIdx.PartitionParentOID = parentIdx.OID` and call `RegisterIndexPartitionChild`,
   mirroring the already-correct idiom at `:6846`/`:8610` rather than inventing one.
   These are the same twin sites 0005f edited — Rule #2 applies again and both changed.
2. `internal/executor/deferred_unique.go` — `uniqueCheckDeferToCommit` tries the index's
   **own** name first (so naming a partition's own constraint directly keeps working),
   then walks `PartitionParentOID` child→root trying each ancestor's name against the
   session map. The walk is bounded by a new `maxPartitionParentWalk = 64`, mirroring
   `maxCTIDChainWalk`'s idiom, so a corrupt/cyclic linkage cannot spin.

### 14.3 Why goopg walks OIDs where PG scans names

PG's `AlterConstraint` (`postgres/src/backend/commands/trigger.c:5820-5960`) resolves the
name primarily by a `(conname, connamespace)` scan, because **PG reuses the same
`conname` for a partition's constraint clone** (verified live against the 18.3 oracle),
with a secondary `conparentid` descent for deeper hierarchies. goopg's clones are named
*differently* from their parent, so porting PG's by-name scan would find nothing. The
OID walk is the correct goopg-side adaptation; do not "fix" this by renaming the clones —
that would be a schema-visible change with its own `pg_constraint` fallout.

### 14.4 Measurement — read this before comparing numbers

`parted_uniq` matches in `tmp/regress-diffs/constraints.diff`: **5 → 0**. The aggregate
count is **1164 lines / 33 hunks both before and after** — unchanged. This was measured
as a true counterfactual (stash only the two production files, re-run the regress
runner, restore), *not* inferred from a post-fix reading; the first measurement pass
concluded from post-fix-only data that an earlier slice had already cleared the hunk,
and that conclusion was wrong. **A single post-fix measurement cannot attribute a
metric change.** The aggregate staying flat while the targeted content vanishes is the
expected shape here: the resolved hunk is replaced by an equal-length unrelated
divergence further down the file.

### 14.5 Guards

Added to the existing `internal/testport/partitioned_deferrable_unique_test.go` (the
0005f file, deliberately extended rather than duplicated):

- `TestPort_PartitionedDeferrableUniqueDefersToCommitViaSetConstraintsNamed` — the named
  parent form defers to COMMIT. **FAIL-pre proven** by reverting only the two production
  files: fails at the INSERT with `duplicate key value violates unique constraint
  "parted_uniq_tbl_1_i_key" (23505)`.
- `TestPort_PartitionedDeferrableUniqueNamedChildOwnConstraintStillDefers` — naming the
  child's own constraint directly still defers, pinning the child-name-first ordering.
  Note this guard passes pre-fix by construction (`idx.Name` was already tried first);
  it is a **regression** guard for the ordering, not a FAIL-pre guard, and is recorded
  as such rather than being presented as evidence the fix works.

The negative direction (no `SET CONSTRAINTS` ⇒ still errors at INSERT) is already
covered by 0005f's `TestPort_PartitionedNonDeferrableUniqueErrorsAtInsert`, which was
re-run green rather than duplicated.

### 14.6 Two divergences deliberately NOT absorbed

- **Deferred FK checks on partitioned tables are broken by a different and worse
  mechanism** (filed as M0134-0005h). The probe found goopg creates *no* per-partition
  FK constraint clone at all (`pg_constraint`: 1 row vs PG's 2, and PG reuses the same
  `conname`), and `runAllDeferredFKChecks` / `fullTableFKCheck`
  (`internal/executor/operators_fk.go:430` / `:462`) resolve the FK owner by name and
  scan the **partitioned root**, which has zero physical blocks — every row lives in a
  leaf. So the COMMIT-tier recheck silently scans nothing and a deferred partitioned-FK
  transaction **commits where PG raises 23503**. This is a silent integrity gap of the
  same class as 0005e, not a message divergence. Wiring `PartitionParentOID` does not
  fix it; a non-partitioned deferred FK works correctly (sanity control).
- **`SET CONSTRAINTS` does no catalog validation at all** —
  `session.go:383` / `operators_tx.go:637` accept any name silently. PG raises `42704
  constraint "%s" does not exist`, and `ERRCODE_WRONG_OBJECT_TYPE` when a non-deferrable
  constraint is named under `DEFERRED`. Ledgered; independent of this slice.

## 15. M0134-0005h — FK scans over a partitioned child never reached the leaves

### 15.1 The divergence

Same class as §12 (0005e): a **silent wrong answer**, not a message divergence. goopg
committed a transaction PG 18.3 rejects.

```sql
CREATE TABLE fk_parent (id int PRIMARY KEY);
CREATE TABLE fk_child (pid int REFERENCES fk_parent(id)
                       DEFERRABLE INITIALLY DEFERRED) PARTITION BY RANGE (pid);
CREATE TABLE fk_child_p1 PARTITION OF fk_child FOR VALUES FROM (0) TO (100);
BEGIN; INSERT INTO fk_child VALUES (42); COMMIT;   -- 42 is NOT in fk_parent
```

PG raises `23503` at COMMIT naming the **leaf** (`fk_child_p1`); goopg committed
cleanly, leaving a violated FK on disk. The same shape held for the DDL tier:
`ALTER TABLE fk_child ADD CONSTRAINT … FOREIGN KEY (pid) REFERENCES fk_parent(id)`
over a partitioned root already holding a violating leaf row was silently **accepted**.

### 15.2 Root cause — §14.6's claim, confirmed but narrowed

§14.6 said the queue "resolves the FK owner by name and scans the partitioned root".
The *observation* is right; the *sizing* was again too large. Two refinements the probe
established before any code was written:

- **What is queued is correct and must not change.** `DeferredFKCheck.ChildTableName`
  (`internal/executor/session.go:17`) records the **root**, set at `operators_fk.go:129`
  from `fkOwnerTbl = o.plan.Table` (`operators_storage.go:2558`). That matches how the
  two already-correct sibling scans work. The bug is entirely at SCAN time.
- **The recursive walk already existed and was already used twice in the same file.**
  `allDescendants(im, tbl, snapEpoch)` (`operators_fk.go:950`) does the
  inheritance+partition BFS with detach-pending filtering; `scanTableForMatch` (`:1000`)
  and `scanTableForMatchFKWait` (`:1370`) both call it with `snapDetachEpoch(ctx)`.
  Only `fullTableFKCheck` (`:465`) was written to scan the single table object handed
  to it. A partitioned root has zero physical blocks, so its loop over `NBlocks` ran
  zero iterations — the check "passed" by scanning nothing.

This is the same lesson as §14: **probe the producer before believing a "needs new
infrastructure" claim.** §14.6 sized this from the consumer plus the PG oracle and
inferred missing machinery; the machinery was three functions away in the same file.

### 15.3 The fix

Both scans split into a per-relation helper, then loop root + `allDescendants`:

- `internal/executor/operators_fk.go` — `fullTableFKCheck` → new
  `fullTableFKCheckRel(ctx, ownerTbl, leafTbl, fk)`.
- `internal/executor/operators_ddl.go` — `validateFKConstraintExistingRows` → new
  `validateFKConstraintExistingRowsRel` (the Rule-#2 DDL twin, serving `ADD FOREIGN
  KEY` / `VALIDATE CONSTRAINT` / `ALTER CONSTRAINT … ENFORCED`).

The twin was **live-probed before being touched**, not assumed from code shape: it
reproduced independently (goopg accepted the `ADD CONSTRAINT`, PG raised 23503 naming
`fk_child_p1`). Had it not reproduced it would have been ledgered, not edited.

Two details that are load-bearing:

- **Per-relation column resolution.** The original body closed over `childTbl.Columns`
  for `DecodeHeapTupleRow` and resolved FK column positions against that list. A
  partition's column order can differ from its root's, so the helper resolves columns
  by NAME against each scanned relation's own `Columns`/`RelFileNode`. Hoisting a
  positional index computed once on the root would have been a fresh silent-corruption
  bug.
- **`assertParentExists`'s `reportTbl` now carries the scanned leaf** (it was
  self-referential to the root). This is what makes the message name `fk_child_p1`, and
  the leaf-naming was verified byte-for-byte against the live 18.3 oracle rather than
  inferred from `ri_triggers.c`.

Non-`*catalog.InMemory` catalogs fall back to root-only, guarded exactly as
`scanTableForMatch` guards it — unchanged behaviour, no new failure mode.

### 15.4 Why PG cannot have this bug

PG recurses FK creation over partitions in
`postgres/src/backend/commands/tablecmds.c` and installs **per-leaf RI triggers**
(`utils/adt/ri_triggers.c`), so PG's deferred after-trigger queue naturally holds
*leaf* entries — there is no "scan the owner" step to get wrong. goopg queues one
root-scoped entry instead, which is a legitimate design difference, but it makes the
leaf expansion the scan's own responsibility. Expect this shape anywhere goopg holds
one root-scoped record where PG holds one record per leaf.

### 15.5 Guards

`internal/testport/partitioned_deferred_fk_test.go` — 7 `TestPort_*` tests:
the COMMIT-tier positive guard, the DDL-twin positive guard, a **multi-level**
partitioning case (partition of a partition — `allDescendants` is recursive and this
proves it, `fk_child_p1_p1`), a satisfied-FK negative guard (partitioned + deferred
must still commit cleanly), and the non-partitioned deferred control (must not
regress). The three positive guards are FAIL-pre/PASS-post, proven by stashing only
the two production files: each failed with `expected 23503 … got <nil>` — i.e. the
pre-fix behaviour was a *silent success*, which is exactly why a one-directional
message-diff guard would not have caught it.

### 15.6 Measurement — and why it is zero

`constraints` regress diff: **1164 lines / 33 hunks, UNCHANGED** from 0005g. That is
the honest result and it is expected: this bug was found by the 0005g *probe*, not by
the `constraints` case, which never exercises a partitioned deferred FK. The slice's
value is the closed integrity gap, not a line delta. Recording it as a diff win would
have been false attribution — the same error §14 already made once in the other
direction. The remaining 33 hunks are NOT-NULL-constraint inheritance-validate and
`COMMENT ON CONSTRAINT` gaps.

### 15.7 Deliberately not absorbed

- **No per-partition `pg_constraint` FK clone row.** PG has one row per leaf reusing
  the parent's `conname`; goopg still has only the root's. Needed for introspection /
  `pg_dump` parity, **not** for enforcement (which §15.3 now handles). Separable and
  larger — ledgered.
- **`fkCascadeDelete` walks `im.PartitionChildren` one level only** where
  `allDescendants` recurses, so `ON DELETE CASCADE` under multi-level partitioning
  misses grandchildren. Same fix idiom, different function; ledgered.
- **`cloneAndValidateAttachPartitionFKs` was probed and found NOT affected** — it
  always passes the real leaf as both scan target and storage owner. Recorded so a
  later loop does not re-probe it.

## 16. M0134-0005i — `ALTER TABLE … SET/ADD NOT NULL` never cascades to existing children

### 16.1 Why this slice, and how it was chosen

Sections §11.4 and §15.6 both *guessed* at what dominated the remaining diff. This
slice was chosen instead from a **fresh measured bucket census** (probe
`tmp/ralph-handoffs/m0134-0005i-probe`), regenerated with:

```
GOOPG_CG_UNIT=m0134-0005i-probe scripts/pg-regress-runner.sh --verbose constraints
```

Baseline re-confirmed at **1164 lines / 33 hunks**. Ranked buckets:

| # | bucket | ≈lines | class |
|---|---|---|---|
| 1 | **`SET`/`ADD NOT NULL` never propagates to existing children** (+ its ripples: `VALIDATE`/`DROP`/`COMMENT ON CONSTRAINT … does not exist` on the child, wrong `conislocal`/`coninhcount`, `\d+` output, ATTACH PARTITION) | **~690** | metadata + integrity-adjacent |
| 2 | `DEFAULT -1 * currval('insert_seq')` silently evaluates to NULL — corrupts SELECT output **and** lets `CHECK(x+z=0)` pass violating rows via 3-valued logic | ~150–180 | **silent wrong answer** |
| 3 | GiST `circle` default opclass missing ⇒ whole `circles` EXCLUDE block never creates its table | ~60 | pre-existing (§11.4 Bucket 5) |
| 4 | multi-unnamed-CHECK auto-naming picks the wrong column; `COPY FROM` does not re-check CHECK | ~40 | silent wrong answer |
| 5 | `ADD EXCLUDE` misses existing violations; ON CONFLICT arbiter wording | ~25 | mixed |
| 6 | partitioned FK naming on ATTACH (`dummy_constr_1` vs `dummy_constr`); `SET CONSTRAINTS` message ordering | ~25 | message |
| 7 | `DEFAULT` uses `a_expr` not `b_expr`, so `DEFAULT 1 IN (1,2)` parses where PG rejects | ~10 | grammar |
| 8 | `DEFAULT_TBL` float8 `123.456` prints as `123` | ~2 | formatting |

The previous loop's second hypothesis — that `COMMENT ON CONSTRAINT … does not exist`
was an independent driver — is **REFUTED**: it is a downstream symptom of bucket 1
(the constraint was never created on the child at all). The genuinely independent
COMMENT wording diffs are ~10 lines and out of scope. Bucket 2 is the largest *silent
wrong-answer* bucket and is the natural successor slice; it is deliberately **not**
conflated here (default-expression evaluation, an unrelated subsystem).

### 16.2 Root cause

`internal/executor/operators_ddl.go`:

- `case parser.AlterTableSetNotNull:` (~:9661) and `case parser.AlterTableAddNotNull:`
  (~:9754) each mutate **only `tbl`** — they set `tbl.Columns[colIdx].NotNull` and call
  `tbl.AddNotNull(…)` on `tbl` alone. Neither consults
  `catalog.InMemory.InheritanceChildren` / `PartitionChildren`.

The machinery is not missing — it is **unwired**, the same shape as §14 and §15:

- The sibling **DROP** path (`~:10917-10960`) already cascades, looping
  `im.PartitionChildren(tbl.OID)` and calling `o.clearNotNullConstraint(childTbl, …)`
  (`~:10947-10957`). ADD/SET cascading and DROP cascading is an asymmetric pair — the
  asymmetry *is* the bug.
- `CREATE TABLE … INHERITS` already seeds a child's NOT-NULL constraints from its
  parent at creation time (`~:3925-3977`, `~:4756-4773`), including the
  merge-not-duplicate accounting. The gap is exclusively the **post-hoc ALTER on a
  parent that already has children**.

### 16.3 PG oracle

`postgres/src/backend/commands/tablecmds.c:7913` `ATExecSetNotNull()`:

- `:8062-8079` — when `recurse`, it calls `find_inheritance_children()` and re-invokes
  **itself** per child with `recursing = true` and the **same constraint name** chosen
  for the parent. Because each child re-enters the same function, the cascade is
  **recursive**, not one level: grandchildren are reached.
- `:7971-7993` — on a child that already carries a NOT-NULL constraint for that column,
  PG **increments `coninhcount`** on the existing row rather than creating a duplicate.
  The child's row ends `conislocal = f, coninhcount = 1` when it was purely inherited,
  which is exactly what the diff's `constr_child2` / `notnull_tbl4_cld*` hunks expect.
- PG 18 unified `SET NOT NULL` and named `ADD CONSTRAINT … NOT NULL` into this **one**
  function. goopg has **two** handlers, so they are a Rule-#2 twin pair: both must
  change, and they must share one cascade helper — otherwise the next loop inherits a
  fresh divergence between them.

### 16.4 Design decisions taken before briefing

1. **Recursive, not one-level.** PG recurses; so must goopg. The DROP sibling's
   single-level `PartitionChildren` loop is *not* the model to copy verbatim — that
   same one-level shortcoming is already a ledgered defect elsewhere
   (`fkCascadeDelete`, §15.7). Walk both inheritance and partition children
   transitively, with a depth/visited bound.
2. **Resolve the column BY NAME per child.** A partition's (or inheritance child's)
   column order can differ from its parent's; hoisting a parent-computed positional
   index would be a fresh silent-corruption bug. This is the §15.3 lesson restated.
3. **Merge, do not duplicate.** Child already has a NOT-NULL constraint for the
   column ⇒ increment its inherited count only. Child has none ⇒ create it with the
   **parent's** constraint name, `IsLocal = false`, inherited count 1.
4. **Bucket 2 is out of scope** and is the designated successor slice.

### 16.5 Status

Briefed as `tmp/ralph-handoffs/m0134-0005i-notnull-inherit-cascade`. Outcome, measured
line delta, and any deviations are recorded in the fix_plan item and in §16.6 once the
slice lands.

### 16.6 Outcome (LANDED 2026-08-18)

One new shared helper `cascadeNotNullToChildren` / `cascadeNotNullToChildrenAt`
(+ `const maxNotNullCascadeDepth = 64`, mirroring `maxCTIDChainWalk` /
`maxPartitionParentWalk`) in `internal/executor/operators_ddl.go`, called from **both**
`case parser.AlterTableSetNotNull:` and `case parser.AlterTableAddNotNull:` — the
Rule-#2 twin pair PG collapses into a single function. +123 production lines; no new
`catalog` field, no new catalog column. All four §16.4 decisions held as designed.

**Measured: `constraints` 1164 → 1122 lines (−42), hunks 33 → 33**, established as a
true stash-based counterfactual (measure post-fix, stash the production file only,
re-measure, restore) — a single post-fix reading is not attribution, an error already
made twice in this milestone.

Guards, `internal/testport/notnull_inherit_cascade_test.go`, 6 `TestPort_*`:
`…SetNotNullCascadesToChildren`, `…AddConstraintCascadesUnderParentName`,
`…CascadesMultiLevel` (the grandchild case — this is what proves recursion),
`…CascadeMergesExistingChildConstraint` (asserts exactly one `pg_constraint` row),
`…CascadeSkipsUnrelatedSibling`. The first three are proven FAIL-pre/PASS-post by
stashing only `operators_ddl.go`; **the pre-fix failures are silent SUCCESSES**
(`INSERT INTO ch (a) VALUES (NULL) succeeded after parent SET NOT NULL; want 23502`),
which is precisely why no message-diff guard would ever have caught this and why the
merge and negative guards were mandatory.

**Brief correction worth keeping:** this brief's criterion 2 wrote the named form as
`ADD CONSTRAINT p_a_nn NOT NULL (a)`. That parenthesised spelling is not valid PG or
goopg grammar — `postgres/src/test/regress/sql/alter_table.sql:915,917` uses the
unparenthesised `NOT NULL a`. The implementer corrected it rather than working around
it.

Deferred, ledgered 2026-08-18: bucket 2 (`DEFAULT … currval()` → NULL, the successor
slice), a missing `NO INHERIT` cascade-skip negative guard, and the surviving
partition-ATTACH `convalidated` + independent `COMMENT ON CONSTRAINT` wording lines.
