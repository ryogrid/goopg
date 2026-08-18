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

## 17. M0134-0005j — a column omitted from an *explicit* INSERT column list loses its DEFAULT

### 17.1 Why this slice

Bucket 2 of the §16.1 measured census: the largest remaining **silent wrong-answer**
bucket. Probe: `tmp/ralph-handoffs/m0134-0005j-probe`.

`constraints.sql` declares

```sql
CREATE TABLE INSERT_TBL (
  x INT DEFAULT nextval('insert_seq'),
  y TEXT DEFAULT '-NULL-',
  z INT DEFAULT -1 * currval('insert_seq'),
  CONSTRAINT INSERT_CON CHECK (x >= 3 AND y <> 'check failed' AND x < 8),
  CHECK (x + z = 0));
```

On goopg `INSERT INTO INSERT_TBL(y) VALUES('Y')` leaves `z` **NULL** with no error.
That is wrong twice over: the SELECT output is corrupt, and `CHECK (x + z = 0)`
then *accepts the violating row*, because a NULL operand makes the predicate NULL
and PG rejects only FALSE (`postgres/src/backend/executor/execMain.c:ExecConstraints`).
goopg's own `checkConstraints` (`internal/executor/operators_fk.go:1699`) already
implements that NULL-passes rule correctly — so the constraint layer is **not** at
fault, and the previously-suspected "CHECK is broken" reading is refuted. Measured
attribution: ~26 lines of the 1122-line `constraints` diff (~24 for the NULL, ~2 for
the `currval` off-by-one below).

### 17.2 Root cause — a planner gap, not an evaluator gap

goopg has **two** default-expression evaluators, and the crippled one is reached only
for one statement shape:

- **Working path.** `internal/optimizer/planner.go:157-165` (`planStmt`, `*parser.InsertStmt`)
  calls `rewriteInsertDefaultMarkers` (`planner.go:9876`) *before* `analyzer.Analyze`,
  substituting `col.DefaultExpr` straight into the INSERT target list. The analyzer
  then binds it like any other cell, so at run time the ordinary
  `evalExpr(…, ctx)` (`internal/executor/expr.go:331`) evaluates it — `currval`
  works, because it is just a normal function call.
- **Broken path.** That rewrite only pads omitted trailing columns when
  `len(s.Columns) == 0` (`planner.go:9922-9934`). With an **explicit** column list
  that omits a column, `colIndex` is built purely from `s.Columns`
  (`planner.go:9899-9909`) and the omitted column never enters the target list at
  all. `insertOp` therefore patches it after the fact via `applyDefaultsForMissing`
  (`internal/executor/operators_generated.go`), a hand-rolled mini-AST-walker
  inherited from the `GENERATED ALWAYS AS` feature. Its `evalGenFuncCall`
  (`operators_generated.go:193-240`) special-cases only `nextval` and zero-arg
  builtins; **every other function call falls through to `return NullDatum, nil`**
  at `:239` — silently, no error. `evalGenBinary` (`:243`) then multiplies NULL and
  also yields NULL.

So the failure is not "`currval` is unimplemented" — it is that a *second, permanently
incomplete* evaluator exists at all. Cause (b) of the probe's four candidates;
(a) parse/store, (c) `currval` itself, and (d) arithmetic were each ruled out by
narrowing (`DEFAULT currval('s')` alone reproduces; `INSERT ... DEFAULT VALUES`
on the same table yields the correct `-1`, because it takes the working path).

**Secondary bug, same mechanism.** The mini-evaluator's inline `nextval` handling
calls `s.nextVal()` directly, bypassing `evalNextval`
(`internal/executor/operators_sequence.go:639-689`) and therefore never writing
`ctx.CurrSeqVals` (`internal/executor/context.go:462`), which `evalCurrval`
(`:694-710`) reads. The regress diff shows `currval('insert_seq')` returning 7 where
PG returns 8. Routing through the planner fixes this for free — no separate change.

### 17.3 Design decisions taken before briefing

1. **Fix the planner, not the mini-evaluator.** Adding `currval` to
   `evalGenFuncCall` would be ~10 lines and would leave a second evaluator that
   silently NULLs the *next* unhandled builtin. That is this project's Rule-#2
   sibling-drift trap in its purest form (a silent wrong answer, not an error), and
   it is explicitly rejected. Extending `rewriteInsertDefaultMarkers` to the
   explicit-column-list-omits-a-column shape retires `applyDefaultsForMissing` for
   **both** INSERT call sites (`operators_storage.go:2440`,
   `operators_upsert.go:213`) at once, and fixes the `currval` session-state bug as
   a side effect.
2. **The two INSERT sites are a Rule-#2 twin pair.** Plain INSERT and
   `INSERT … ON CONFLICT` share the gap (already noted as siblings in the `root-0020`
   comment at `operators_upsert.go:199-205`). Both must be covered by tests; the
   plan-time fix covers both by construction, which is a further argument for it.
3. **Serial/identity interaction is the live hazard.** A `serial` column's
   `DefaultExpr` *is* `nextval(...)`, and `autoGenerateSerialValues` also fills such
   columns at run time. Substituting at plan time must not cause a **double
   advance** of the sequence. This is the slice's primary regression risk and gets an
   explicit guard test.
4. **The apply worker is out of scope and deferred.** `applyworker.go:286` decodes
   binary pgoutput tuples and has no SQL statement for the planner to rewrite, so it
   is the one call site that would need a runtime bind-and-eval
   (the `checkConstraints` synthetic-SELECT + Plan pattern, `operators_fk.go:1699`).
   Ledgered, not bundled.
5. **`computeGeneratedColumns` keeps its own swallow.** It shares the evaluator and
   also discards errors (`operators_generated.go:33-47`); GENERATED semantics are a
   different question from DEFAULT and are not re-litigated here. Ledgered.

### 17.4 Separately discovered, out of scope

- `internal/executor/copy.go:323` `insertSourceRow` never calls
  `applyDefaultsForMissing` at all: **COPY with a partial column list drops every
  DEFAULT expression**, not merely function calls. It contributes 0 lines to this
  diff (`constraints.sql`'s `COPY_TBL` has no DEFAULTs), but it is a larger
  independent gap. Ledgered.
- The same `@@ -162,107 +160,101 @@` hunk also bundles three unrelated, unresearched
  bugs: inherited-CHECK naming/enforcement on `INSERT_CHILD` (~19 lines), `tableoid`
  inside a CHECK (~15 lines), and missing DDL-time rejection of `ctid` in a CHECK
  (~5 lines). Deliberately not conflated.

## 18. M0134-0005k — NOT NULL constraint-name / NO INHERIT conflict validation is absent

### 18.1 Why this slice, and how it was chosen

Chosen from a **fresh measured bucket census** at HEAD `442863f6` (probe
`tmp/ralph-handoffs/m0134-0005k-census`), regenerated with:

```
GOOPG_CG_UNIT=m0134census scripts/pg-regress-runner.sh --verbose constraints
```

New baseline: **1024 lines / 31 hunks** (down from the §16.1 1164/33; **never
compare to a pre-2026-08-18 number**). Re-ranked buckets:

| # | bucket | ≈lines | class |
|---|---|---|---|
| 1 | **NOT NULL constraint-name / NO INHERIT conflict validation entirely absent** — PG rejects 14 `notnull_tbl_fail` shapes; goopg accepts every one, then cascades into `relation already exists` + phantom columns for the rest of the script | **~500 (≈49%)** | **silent wrong answer** |
| 2 | `INSERT … SELECT` with an explicit column list drops the DEFAULT (`s.Select != nil` early return in `rewriteInsertDefaultMarkers`) | ~55 | **silent wrong answer** (ledgered §17.3) |
| 3 | `COPY` with a partial column list drops every DEFAULT and skips CHECK (`copy.go:323`) | ~20 | **silent wrong answer** (ledgered §17.4) |
| 4 | GiST `circle` default opclass missing | ~65 | pre-existing |
| 5 | `DEFAULT 1 IN (1,2)` a_expr/b_expr grammar gap | ~15 | grammar |
| 6 | `tableoid`/`ctid` inside a CHECK | ~15 | silent wrong answer |
| 7 | `pp_nn` `NOT NULL a` + `PARTITION BY` parser ordering | ~10 | parser |
| 8 | missing `pg_get_partition_constraintdef` builtin | ~15 | clean error |
| 9 | misc cosmetic (SET CONSTRAINTS/COMMIT ordering, FK-naming suffix, ON CONFLICT wording) | ~25 | message |

Bucket 1 is both the largest bucket *and* a silent-accept integrity gap, so it is
this slice. Buckets 2 and 3 stay ledgered; the `applyworker.go:286` mini-evaluator
gap (§17.3) is **confirmed absent from this diff** — `constraints.sql` never drives
the logical-replication apply path, so it needs the pgoutput harness, not a psql
snippet, and remains ledgered.

### 18.2 The four PG error conditions (oracle)

All 14 failing shapes reduce to three distinct messages plus a partitioned-table
variant. Verbatim from `postgres/src/test/regress/expected/constraints.out`:

| # | message | errcode | PG site |
|---|---|---|---|
| E1 | `conflicting not-null constraint names "%s" and "%s"` | (elog / 42601) | `parse_utilcmd.c:802` (column-level, `elog`), `heap.c:2982` (table-level, `ERRCODE_SYNTAX_ERROR`) |
| E2 | `conflicting NO INHERIT declarations for not-null constraints on column "%s"` (**plural**) | 42601 | `parse_utilcmd.c:773` and `:808` — column-level only |
| E3 | `conflicting NO INHERIT declaration for not-null constraint on column "%s"` (**singular**) | 42601 | `heap.c:2968` — table-level merge only |
| E4 | `not-null constraints on partitioned tables cannot be NO INHERIT` | 0A000 | `parse_utilcmd.c:759` (column-level), `:1077` (table-level) |

The singular/plural split is **not** cosmetic: it is how PG distinguishes the
column-constraint path (`transformColumnDefinition`) from the table-constraint
merge (`AddRelationNotNullConstraints`). Both spellings appear in the expected
output, so goopg must reproduce both, from the corresponding path.

### 18.3 The two PG algorithms to mirror

**(A) Column-level — `parse_utilcmd.c:transformColumnDefinition`.** A pre-scan
sets `disallow_noinherit_notnull` when the column also carries `PRIMARY KEY` or
`IDENTITY` (`CONSTR_PRIMARY` / `CONSTR_IDENTITY`). Then, per `CONSTR_NOTNULL`
constraint on the column, in order:
1. partitioned && `is_no_inherit` → **E4**;
2. `disallow_noinherit_notnull && is_no_inherit` → **E2** (this is what rejects
   `a int primary key constraint foo not null no inherit` and
   `a int not null no inherit primary key` — order-independent, because the
   pre-scan runs first);
3. first NOT NULL on the column becomes `notnull_constraint`; each **subsequent**
   one is compared against it — differing non-empty names → **E1**; differing
   `is_no_inherit` → **E2**; an unnamed first constraint **adopts** the later name.

**(B) Table-level merge — `heap.c:AddRelationNotNullConstraints`.** Over the list
of table-constraint NOT NULLs (which already includes the column-level ones
promoted by (A), and any copied in by `LIKE`), for each entry scan the remainder:
same column ⇒
1. differing `is_no_inherit` → **E3**;
2. `other` named and `constr` unnamed → adopt; both named and different → **E1**;
3. duplicate is deleted from the list (a column gets exactly ONE not-null row).

Order matters: (A) runs first and raises the *plural* E2, (B) second and raises
the *singular* E3. `a int primary key, not null a no inherit` therefore yields E3
(the PK's implicit NN and the table-level NN merge), while
`a int primary key constraint foo not null no inherit` yields E2 (both on the
column).

### 18.4 goopg state before the slice

- **The catalog model is already sufficient.** `catalog.NamedNotNullConstraint`
  (`internal/catalog/catalog.go:240`) carries `Name`, `ColName`, `OID`,
  `NoInherit`, `NotValid`, and `Table.AddNotNull` (`:254`) threads all of them.
  **No model extension is needed** — this is the sixth "unwired, not missing"
  finding in this milestone.
- **Column-level parsing is present**: `parser.ColumnDef.NotNullNoInherit`
  (`ast.go:1248`) and `NotNullConstraintName` (`:1255`).
- **Table-level parsing is MISSING**: there is no CREATE TABLE AST field for
  `[CONSTRAINT name] NOT NULL col [NO INHERIT]` as a *table* constraint (the form
  exists only for `ALTER TABLE … ADD`, `AlterTableAddNotNull`, `ast.go:3054`).
  Adding it is a prerequisite for 5 of the 14 shapes and for bucket 7.
- **No validation exists anywhere**: none of E1–E4 appears in `internal/`
  (grep-confirmed). `Table.AddNotNull` appends unconditionally.

### 18.5 Slice boundary

In scope: the parser's table-level NOT NULL constraint form, algorithms (A) and
(B) above, and E1–E4 — enough to make all 14 `notnull_tbl_fail` statements fail
with PG's exact message, which also stops the downstream catalog corruption
(`relation already exists`, phantom columns) that inflates this bucket.

Out of scope (ledger, not this slice): the inherited-parent branch of
`AddRelationNotNullConstraints` (`coninhcount`/`conislocal` accounting and the
"a parent already has one, so refuse NO INHERIT" rule), `ALTER TABLE … INHERIT`
not-null satisfaction, and ATTACH/DETACH `convalidated` accounting (already
ledgered under M0134-0005i).

### 18.6 Outcome (LANDED 2026-08-18)

`constraints` regress diff **1024 → 933 lines** (−91) at unchanged 31 hunks; the
`notnull_tbl_fail` block and the two `NOT NULL NO INHERIT` acceptance blocks are
now diff-free. **14/14** shapes match PG byte-for-byte, including the LIKE case
that §18.5 allowed to be left failing — goopg's LIKE path does copy the constraint
name, so no exception was needed.

Landed:
- **Parser** (`internal/parser/ast.go`, `ddl.go`): `ColumnDef.NotNullExplicit` plus
  the parallel table-constraint slices `CreateTableStmt.TableNotNullNames` /
  `TableNotNullCols` / `TableNotNullNoInherit` (mirroring the existing
  `TableChecks`/`TableCheckNoInherit` style). `parseTableConstraintElement` gained
  the `CONSTRAINT name NOT NULL col [NO INHERIT]` case — **previously parsed and
  then silently dropped**. `parseColumnConstraintList` now accumulates all
  occurrences on a column and defers to a new `resolveColumnNotNull`, which is
  algorithm (A) of §18.3 including the PRIMARY KEY / IDENTITY pre-scan.
- **Executor** (`internal/executor/operators_ddl.go`): new
  `mergeNotNullEntries`/`nnEntry` implement algorithm (B); `execCreateTable`'s
  not-null loop was rebuilt around per-column entry lists and a new E4 pre-check.

Three pre-existing bugs were **exposed** by the new coverage and had to be fixed
for the shapes to fail *correctly* rather than differently-wrong:
1. the partitioned-table NO-INHERIT guard mis-attributed a `NOT NULL NO INHERIT`
   to the CHECK-only `42P16` message;
2. a `colByName`/BodyOrder shadowing bug;
3. the INHERITS column-copy path propagated NOT NULL to children **unconditionally,
   ignoring the parent's NO INHERIT flag** — acceptance criterion 2 (the `ATACC1`/
   `ATACC2` block) could not pass without it.

Confirmed by measurement, not assumption: the catalog model needed no extension
(§18.4), and the round-1 census's `:10523` "ADD COLUMN twin" citation was wrong —
that line is `execAlterTableAddPrimaryKey`. The real `execAlterTableAddColumn`
(`:10145`) turned out to have a *larger* gap (it registers no `pg_constraint` row
at all for an inline named NOT NULL); it is ledgered, not bundled.

Gates: `go build ./...`; `go test ./internal/{parser,catalog,executor,optimizer}/`;
`go test -run 'TestPort_.*(NotNull|Constraint|Inherit)' ./internal/testport/` (18
subtests); `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (**Q12=2, Q13=35** — Rule #1).

## 19. M0134-0005l — `COPY FROM` enforced *no* constraints at all (LANDED 2026-08-18)

### 19.1 The gap

`COPY <table> FROM …` wrote rows straight to the heap. `insertSourceRow`
(`internal/executor/copy.go`) scattered the parsed input columns into a `Row`,
left every other column NULL, and handed it to `writeHeapRowReturning` — the raw
heap writer. Along that path goopg applied **no** column DEFAULTs, enforced **no**
NOT NULL, evaluated **no** CHECK constraints, and checked **no** domain
constraints. This is a *silent integrity gap* of the same class as M0134-0005e and
-0005h: goopg accepted and durably stored rows PostgreSQL rejects outright, and
stored NULL where PostgreSQL stores a default.

PostgreSQL does the opposite of goopg's "fork by statement shape": `CopyFrom`
(`postgres/src/backend/commands/copyfrom.c:1352-1358`) calls `ExecConstraints`
unconditionally for **every** copied row, and builds `defmap`/`defexprs`
(~:1545-1833) so that every column *absent from the COPY column list* gets its
default expression evaluated. There is no bulk-load exemption.

### 19.2 Scope correction found during the census

The slice was briefed from the previous loop's note as "COPY with a **partial**
column list drops DEFAULTs". The census (`tmp/ralph-handoffs/m0134-0005l-census/`)
corrected this against the actual fixture: the exercising case is
`postgres/src/test/regress/sql/constraints.sql:255-267`, a **bare**
`COPY COPY_TBL FROM …` with *no column list at all*, whose CHECK constraint must
reject a row. The defect was therefore never partial-column-list-specific — COPY
FROM skipped constraints unconditionally. Briefing the narrow framing would have
produced a fix that missed the fixture entirely; **re-derive a carried-over
candidate's framing from the fixture before briefing it.**

### 19.3 The fix — reuse, not reimplementation

`insertOp.Next` (`internal/executor/operators_storage.go:2405-2508`) already ran
the correct sequence, and its **order is load-bearing** because PG's error
precedence depends on it:

    applyDefaultsForMissing → NOT NULL loop → checkConstraints → checkDomainConstraintsForRow

`insertSourceRow` now runs exactly that sequence, calling the same four helpers,
before `writeHeapRowReturning`. This is Rule #2 (sibling paths) in its purest
form: the slice existed *because* COPY and INSERT had diverged, so the fix is
wiring, not new logic (~91 lines including comments).

Two details worth preserving:

- **`missing[]` mirrors PG's `defmap`, not "is NULL".** A column *present* in the
  COPY column list but holding an explicit NULL from the input is **not**
  missing — PG substitutes a default only for columns the column list never
  targets. `missing[]` is derived once per statement from `plan.ColumnIndex`
  (all-true for a bare COPY, since `ColumnIndex` then covers and clears every
  column).
- **`needsConstraints` is a per-statement fast path.** COPY is the bulk-load path
  (TPC-H/TPC-DS loads), so the whole sequence is skipped for a table with no
  DEFAULTs, no NOT NULL columns, no CHECK constraints and no domain-typed
  columns. The guard is computed once in `newCopyFromExecutor`, never per row.
  It is a genuine fast path, not a dead branch: `Column.DeclaredTypeName` is
  populated only when the DDL type differs from the resolved base type
  (`internal/catalog/catalog.go:159`), i.e. for domains — an ordinary staging
  table still takes the cheap path.

### 19.4 Measured result

`constraints` regress diff **933 → 923 lines** (hunks unchanged at 31). The target
hunk shrank substantially but did **not** vanish, for a reason unrelated to
constraints — see §19.5.

Guards (all FAIL-pre / PASS-post except the fast-path guard, which is a
performance-regression guard and passes both ways by design), in
`internal/executor/copy_constraints_test.go`:
`TestCopyFromCheckConstraintViolationRejected`,
`TestCopyFromDefaultFilledForOmittedColumn`,
`TestCopyFromNotNullViolationRejected`,
`TestCopyFromConstraintFreeTableFastPath`.

Gates: `go build ./...`; `go test ./internal/{executor,optimizer,catalog}/`;
`go test -run 'TestPort_.*(Copy|Constraint)' ./internal/testport/` (12);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (**Q12=2, Q13=35** — Rule #1).

### 19.5 Why the hunk only shrank — the COPY error-reporting holdout

The remaining diff lines in that hunk are the CHECK violation's `DETAIL: Failing
row contains (…)` and `CONTEXT: COPY <table>, line N: "…"` trailers. goopg now
raises the right error with the right SQLSTATE, but drops both trailers on the
floor: the nine COPY-FROM error call sites in `internal/postmaster/copy.go` call
`s.writeQueryError(w, execErrCode(perr), execErrMsg(perr))` **without**
`execErrDetailFields(perr)...`. Every other error site (`dispatch.go`) already
forwards it — COPY FROM is the lone holdout, so `ExecError.Detail`/`.Context`
never reach the wire. This is pre-existing, affects *all* COPY FROM errors rather
than constraints specifically, and additionally needs input-line tracking to
synthesize `CONTEXT` at all. Deliberately not bundled (third file, nine call
sites, different subsystem) — ledgered 2026-08-18.

### 19.6 Sibling paths still bypassing constraints (ledgered)

`writeHeapRowReturning` has other callers that were **not** touched by this slice
and are the natural next Rule-#2 sweep. The confirmed one is
`PushBinaryData` in the same file — the **COPY BINARY** path bypasses
`insertSourceRow` entirely and therefore still skips every default and
constraint. Flagged but uninvestigated: `applyworker.go`, `operators_merge.go`,
`operators_storage.go` (partition/UPDATE paths), `operators_ddl.go`,
`operators_upsert.go`. Ledgered 2026-08-18.

## 20. M0134-0005m — `INSERT … SELECT` loses omitted columns' DEFAULT expressions

### 20.1 Symptom

`tmp/regress-diffs/constraints.diff:110-166`, three hunks (the third being pure
downstream corruption of the second). Fixture
`postgres/src/test/regress/sql/constraints.sql:103-129`:

```sql
CREATE TABLE INSERT_TBL (x INT  DEFAULT nextval('insert_seq'),
                         y TEXT DEFAULT '-NULL-',
                         z INT  DEFAULT -1 * currval('insert_seq'), …);
…
INSERT INTO INSERT_TBL(y) SELECT yd FROM tmp;
```

PG fills `z` per row (`-4, -5, -6`); goopg leaves it NULL. Note `x` **is**
filled — the sequence path covers it — so the gap is specific to general
DEFAULT *expressions* under the `INSERT … SELECT` shape. The `INSERT INTO
INSERT_TBL SELECT * FROM tmp` (no column list, SELECT narrower than the table)
form at `constraints.sql:~119` has the same hole.

### 20.2 Root cause — *not* "DEFAULT is never attempted"

The obvious reading (`rewriteInsertDefaultMarkers` bails at
`internal/optimizer/planner.go:9877` on `s.Select != nil`, so nothing happens)
is only half the story, and the wrong half to fix blindly. Two things compose:

1. `planInsert`'s SELECT branch (`internal/optimizer/planner.go:10145-10181`)
   never applies the M0134-0005j column-list extension (`:9916-9945`), so an
   omitted DEFAULT column is simply absent from `Insert.ColumnIndex`.
2. The executor (`internal/executor/operators_storage.go:2405-2440`) derives
   `insertMissing` purely from "ordinal not in `ColumnIndex`", so it **does**
   route `z` to `applyDefaultsForMissing`
   (`internal/executor/operators_generated.go:63`) — but that path uses the
   hand-rolled `evalGenExpr`/`evalGenFuncCall`
   (`operators_generated.go:101-230`), which has **no `currval` case** and
   silently returns `NullDatum, nil`.

So the DEFAULT *is* attempted, through a deliberately lightweight evaluator that
cannot express it. That is the same evaluator gap M0134-0005j documented and
worked around for the VALUES shape (`internal/executor/default_omitted_column_test.go:37-45`).

### 20.3 Decision

Fix **in the planner, in `planInsert`'s SELECT branch**, by routing the DEFAULT
through the *full* expression evaluator rather than by widening the lightweight
one:

- Wrap the planned SELECT (`sel`) in an `optimizer.Project`
  (`internal/optimizer/plan.go:1287-1307`) that passes the SELECT's own output
  columns through (`ColumnRef`, `plan.go:429-435`) and **appends one resolved
  DEFAULT expression per omitted column**, resolved with the same
  `resolveExpr` call the branch already uses at `:10198-10206`.
- Extend `colIndex` in lockstep so `Insert.ColumnIndex` covers the appended
  columns. The executor then sees them as ordinary supplied values and needs
  **zero change** — `insertMissing` is already generic over `ColumnIndex` width.
- Factor the M0134-0005j eligibility predicate (`DefaultExpr != nil &&
  !GeneratedAlways`) into one shared helper used by both the VALUES
  marker-substitution path and the new SELECT Project-append path. This is a
  Rule-#2 twin pair; a private copy in each would drift. The predicate is what
  keeps serial/identity columns out (their `DefaultExpr` is nil by convention),
  avoiding a double sequence advance — see §17.3 decision 3.
- Apply the same treatment to the **no-column-list** SELECT case
  (`:10179-10181`), where the SELECT is narrower than the table.

Rejected: lifting the `s.Select != nil` guard at `:9877` so the marker rewriter
runs for SELECT too. `rewriteInsertDefaultMarkers` operates on `s.Rows` cells;
there are no cells in the SELECT shape, so the guard is structurally correct
and removing it buys nothing. Also rejected: teaching `evalGenFuncCall` about
`currval` — that evaluator is shared with the apply worker (see its doc
comment) and widening it is a larger, riskier surface than the planner fix.

### 20.4 PG oracle

`postgres/src/backend/rewrite/rewriteHandler.c:rewriteTargetListIU` (~:775),
called from `RewriteQuery` (~:4046-4070). PG runs **one** INSERT rewrite path
regardless of source shape; the only branch there is a VALUES-RTE
*optimization*, not a semantic fork. Omitted columns' defaults are appended as
ordinary extra targetlist entries and evaluated normally — which is exactly
what the Project-wrap reproduces.

### 20.5 Out of scope (ledger candidates)

`COPY FROM` and the upsert insert branch reach the same currval-blind
`applyDefaultsForMissing` in principle. Neither is exercised by this fixture;
recorded as a follow-up rather than widened into this slice.

## 21. M0134-0005n — `ALTER TABLE` never validates a NOT-NULL constraint *name*

### 21.1 Why this slice (fresh census, 2026-08-18, HEAD `9ac2ee3d`)

Re-measured with the §1 command
(`scripts/pg-regress-runner.sh --verbose constraints`, probe
`tmp/ralph-handoffs/m0134-0005n-census`): **865 lines / 29 hunks**, down from the
§18.6 933/31 after 0005l+0005m — a clean shrink, no unmasking, no regression.
**Never compare to a pre-2026-08-18 number.**

Re-ranked, the dominant remaining bucket is a **NOT NULL / PRIMARY KEY
bookkeeping cluster** (18 of 29 hunks, ~600 lines) that decomposes into six
sub-bugs. This slice takes sub-bug **1a**, the only one pinned to a specific line
and the one with the largest masking cascade: a phantom column `b` survives a
statement PG rejects and then taints three downstream `\d+ notnull_tbl1` blocks.
The other five (`public.`-qualified sequence deparse; PK not synthesising NOT
NULL under `INHERITS`/`USING INDEX`; missing NO-INHERIT/drop-PK validation;
`pg_get_partition_constraintdef` unimplemented; and `get_nnconstraint_info`
`coninhcount`/`convalidated` drift under multi-level inheritance, ~200 lines and
the largest *silent* sub-bucket) stay unpinned and are **not** to be briefed
until each gets its own research pass — the milestone's recurring lesson is to
size from pinned code, not from the symptom.

### 21.2 The two divergences

Both live in `ALTER TABLE`, both are silent accepts of a statement PG rejects.

**D1 — a differently-named NOT NULL on a column that already has one.**
Fixture `postgres/src/test/regress/sql/constraints.sql:629`; expected
`expected/constraints.out:853-854`:

```
ALTER TABLE notnull_tbl1 ADD CONSTRAINT nn NOT NULL a;
ERROR:  cannot create not-null constraint "nn" on column "a" of table "notnull_tbl1"
DETAIL:  A not-null constraint named "notnull_tbl1_a_not_null" already exists for this column.
```

The statement immediately before it (`:627`, the *same* name re-specified) is a
successful no-op, and goopg already gets that right — the fix must not disturb it.

**D2 — `ADD COLUMN` with an already-used constraint name.**
Fixture `constraints.sql:632`; expected `constraints.out:865`:

```
ALTER TABLE notnull_tbl1 ADD COLUMN b INT CONSTRAINT notnull_tbl1_a_not_null NOT NULL;
ERROR:  constraint "notnull_tbl1_a_not_null" for relation "notnull_tbl1" already exists
```

(42710 `ERRCODE_DUPLICATE_OBJECT`.)

### 21.3 PG oracle — and why the two look unrelated in goopg

In PostgreSQL these are **the same code path**. `parse_utilcmd.c` splits
`ADD COLUMN … CONSTRAINT name NOT NULL` into a synthesised `AT_AddConstraint`
subcommand, so both shapes converge on
`tablecmds.c:ATAddCheckNNConstraint` → `heap.c:AddRelationNewConstraints`
(`heap.c:2385`), which

- for a name already used on the relation raises 42710 from its
  `ConstraintNameIsUsed` check (`heap.c:~2641-2652`) — this is **D2**; and
- for a column that already carries a not-null constraint delegates to
  `pg_constraint.c:AdjustNotNullInheritance` (`:741`), whose three **ordered**
  checks are — this is **D1**:

| order | condition | message | errcode |
|---|---|---|---|
| 1 | `is_no_inherit != conform->connoinherit` | `cannot change NO INHERIT status of NOT NULL constraint "%s" on relation "%s"` (+ HINT) | 55000 |
| 2 | `!is_notvalid && !conform->convalidated` | `incompatible NOT VALID constraint "%s" on relation "%s"` (+ HINT) | 55000 |
| 3 | `is_local && new_conname && strcmp(…) != 0` | `cannot create not-null constraint "%s" on column "%s" of table "%s"` (+ DETAIL) | 55000 |

goopg's parser does **not** perform the `ADD COLUMN` → `AT_AddConstraint` split;
the constraint stays inline on the `ColumnDef`. So the fix cannot be "redirect
ADD COLUMN into the ADD-CONSTRAINT case" — D2 must be fixed where it lives.
That asymmetry is the whole reason this reads as two bugs.

### 21.4 goopg state before the slice

- **D1** — `internal/executor/operators_ddl.go:9911-9989`, the
  `case parser.AlterTableAddNotNull` arm of `execAlterTable` (`:7708`). Its
  existing duplicate check (`:9948-9958`) is keyed on the **column name only**:
  it correctly makes a repeat a no-op, but never compares the incoming name,
  `NoInherit` or `NotValid` against the existing constraint. All **three** checks
  of §21.3 are missing, not just the name one.
- **D2** — `internal/executor/operators_ddl.go:10291-10385`,
  `execAlterTableAddColumn` (line confirmed by re-derivation; the round-1 census
  of slice 0005k cited a line that proved to be a different function, so this was
  checked rather than trusted). `col.NotNullConstraintName`
  (`internal/parser/ast.go:1255`) is never read: there is no duplicate check
  **and no `NamedNotNullConstraint` row is registered at all**, confirming the
  §18.6 ledger note. ADD COLUMN … NOT NULL today sets the attribute flag and
  nothing else.
- **Reuse, not re-derivation**: `fkConstraintNameInUse`
  (`operators_ddl.go:7568-7590`) is already goopg's general "name used on this
  relation" predicate behind other 42710s — D2's check is a call to it.
  `Table.AddNotNull` (`internal/catalog/catalog.go:254`) is the registration both
  correct paths already use. `mergeNotNullEntries`/`nnEntry` and the parser's
  `resolveColumnNotNull` from 0005k are CREATE-TABLE-time only and are **not**
  reusable here.

### 21.5 Slice boundary

In scope: D1's three ordered checks with PG's verbatim messages/HINTs/DETAIL and
55000; D2's 42710 duplicate-name check; and registering the missing
`NamedNotNullConstraint` row in `execAlterTableAddColumn` so ADD COLUMN … NOT
NULL is catalogued like every other path (PG registers for the named *and*
unnamed forms; the duplicate-name check applies only to an explicitly given
name, since PG's `ChooseConstraintName` avoids collisions for synthesised ones).

D1 implements all three checks rather than only the name check the fixture
exercises. They are one PG function, share the existing-constraint lookup this
slice adds anyway, and splitting them would guarantee a second visit — the
milestone has paid that cost twice already.

Out of scope (unpinned; each needs its own research pass first): the five other
sub-bugs of the §21.1 cluster, in particular `get_nnconstraint_info`'s
`coninhcount`/`convalidated` drift.

### 21.6 Outcome (LANDED 2026-08-18)

`constraints` regress diff **865 → 845 lines** (−20) at unchanged **29 hunks** —
no unmasking this time. Both fixture statements now produce PG's ERROR/HINT/DETAIL
exactly, and the phantom column `b` is gone, so the three downstream `\d+
notnull_tbl1` blocks match as well.

Landed in `internal/executor/operators_ddl.go` (two call sites, as scoped):

- **D1** — the `parser.AlterTableAddNotNull` arm gained the existing-constraint
  lookup plus §21.3's three ordered checks with PG-verbatim text. The old
  column-name-keyed duplicate check was folded into the new lookup rather than
  left beside it, so there is one source of truth for "does this column already
  have a not-null constraint".
- **D2** — `execAlterTableAddColumn` gained the 42710 guard (reusing
  `fkConstraintNameInUse`) ahead of the column insertion, and a `tbl.AddNotNull`
  registration afterwards.

**One correction the fixture forced, worth keeping:** the first implementation
put D1's three checks *after* the pre-existing 23502 null-value scan. That is
wrong — in PG the checks live in `AddRelationNewConstraints` (constraint
registration), which runs *before* `ATRewriteTable`'s phase-3 validation scan, so
an incompatible existing constraint must be reported even over a table with no
NULLs, and even when the table would *also* fail the null scan. Only the regress
measurement caught it; the unit tests passed under both orderings. Check ordering
between a catalog check and a table scan is a real observable, not an
implementation detail.

Also confirmed: the name-synthesis expression
(`lower(table)_lower(column)_not_null`) is hand-inlined at five pre-existing sites
in this file; the new registration matches that convention. Extracting a helper
across all six is a legitimate cleanup but was deliberately not bundled here.

Guards: 6 new tests in `internal/executor/operators_ddl_addnotnull_name_test.go`
(each FAIL-pre / PASS-post). `TestAlterTableAddNotNullNamed`
(`operators_ddl_named_check_test.go`) was found to be **asserting the D1 bug** —
it required a differently-named second `ADD NOT NULL` to succeed — and was
corrected; its idempotency assertion was retained under the same-name spelling
rather than deleted (the §20 lesson about renamed tests silently dropping guards).

Ledgered 2026-08-18: PG reaches `AdjustNotNullInheritance` from every path that
attaches a not-null to an existing column, so the NOT-VALID incompatibility check
also belongs on `ALTER TABLE … ADD PRIMARY KEY` and `… ADD GENERATED … AS
IDENTITY`; goopg still accepts both silently. No fixture hunk witnesses it.

Gates: `go build ./...`; `go test ./internal/{executor,catalog,parser,optimizer}/`;
`go test -run 'TestPort_.*(NotNull|Constraint|Inherit)' ./internal/testport/`;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (**Q12=2, Q13=35** — Rule #1).

## 22. M0134-0005o — `ALTER TABLE … ADD PRIMARY KEY` never persisted or cascaded the synthesized NOT NULL (LANDED 2026-08-18)

### 22.1 Why this slice

Selected from §21.1's six-sub-bug NOT-NULL/PRIMARY-KEY cluster — rank #2, "PK not
synthesising NOT NULL". Per §21.1's standing rule the sub-bug was **unpinned**, so
it got its own research pass first (`tmp/ralph-handoffs/m0134-0005o-pknn`) rather
than being sized from the symptom. Census re-measured at HEAD `ecb3e523`:
**845 lines / 29 hunks**, confirming §21.6 unchanged.

That research pass materially changed the slice. The symptom read as "goopg does
not synthesise NOT NULL for a PK"; the code says goopg **does** — since DU-002
slice 50, `execAlterTableAddPrimaryKey` sets `col.NotNull` and registers a
`NamedNotNullConstraint`. Two *other* things were missing, and they are what the
fixture actually witnesses.

### 22.2 The two divergences

**D1 — the flip never reached the on-disk heap.** `pg_attribute` is heap-backed,
not virtual (see the standing note "pg_class is virtual, pg_attribute is heap"),
so an in-memory `col.NotNull = true` is invisible to `\d+` and `pg_dump`, both of
which scan the heap. Every sibling not-null path already ends with the
`catalogHeapSyncAvailable` → `MaterializeWriterXID` → `deleteCatalogRowsForOID` →
`syncTableToCatalogHeap` quadruple (`AlterTableSetNotNull`,
`operators_ddl.go:9869-9877`); the PK path alone omitted it.
Fixture `constraints.sql:729`.

**D2 — no cascade to pre-existing inheritance children.** A child created
*before* the parent's `ADD PRIMARY KEY` never received the constraint, so it
diverged from an otherwise identical child created *after*. M0134-0005i had
already landed `cascadeNotNullToChildren`; the PK path simply never called it.
Fixture `constraints.sql:735-749`.

### 22.3 PG oracle — two injection sites, one terminal function

Not one unified path, which is the §21.3 lesson recurring in the opposite
direction (here PG *splits* what goopg had merged into a single arm):

- Plain `ADD [CONSTRAINT n] PRIMARY KEY` — `tablecmds.c:ATPrepAddPrimaryKey`
  (`:9499`), at execution time, re-entering `ATPrepCmd` with `recurse=true` so
  the synthesized constraint drives the same `find_all_inheritors` cascade as
  `ATExecSetNotNull` (`:8062-8079`).
- `PRIMARY KEY USING INDEX` — `parse_utilcmd.c:transformIndexConstraint`
  (`:2558-2562`), at *parse-analysis* time.
  `catalog/index.c:index_check_primary_key` (`:202`) deliberately does **not**
  auto-synthesize (its own doc comment records the move) and errors when the
  not-null is still missing.
- Both terminate in `ATAddCheckNNConstraint` → `heap.c:AddRelationNewConstraints`
  (`:2385`) — the same function M0134-0005n (§21) already exercises.
- `INHERITS` is **not** a third path: it is recursion off `ATPrepAddPrimaryKey`,
  plus CREATE-TABLE-time not-null copying which goopg already gets right.

### 22.4 Slice boundary

Taken: plain + named spellings (one dispatch arm, `AlterTableAddPrimaryKey` at
`:8043`, serves both) and both `INHERITS` shapes, implemented purely by **reusing
existing helpers** — 24 added lines, no new mechanism.

Deliberately excluded, each ledgered rather than forward-referenced:
`PRIMARY KEY USING INDEX` and its `UNIQUE USING INDEX` twin (parsed to
`AlterTableNoOp` at `parser/ddl.go:9849`/`:10081` — the whole constraint
promotion is unimplemented, pre-existing M0097-0023 debt, far larger than this
slice); the NO-INHERIT-PK compatibility check (already ledgered 2026-08-18 under
§21.6, and blocked behind the `get_nnconstraint_info` coninhcount-drift bucket);
and the `cnn2_parted` partition-PK null scan (`constraints.sql:494`, separate
root cause).

### 22.5 Outcome

`constraints` regress diff **845 → 775 lines (−70)** — the largest single-slice
shrink of this milestone so far, and it came from 24 lines of glue calling code
that already existed. Both target fixture groups leave the diff entirely; the
only remaining hunks in this area are the two explicitly out-of-scope ones above.

Guards: 4 in-memory unit tests
(`internal/executor/operators_ddl_addpk_notnull_test.go` — bare and named
spellings, existing-child cascade incl. `InhCount`, and a no-duplicate guard on
an already-not-null column) plus 3 full-server tests
(`internal/testport/addpk_notnull_test.go`) that query
`pg_attribute.attnotnull` directly — i.e. the same catalog `pg_dump` reads, so
D1 cannot regress unnoticed behind a green in-memory test. FAIL-pre/PASS-post
verified by stashing `operators_ddl.go` alone while keeping the test files.

**The lesson worth carrying:** "feature missing" and "feature present but not
persisted" produce the *same* regress hunk. The census symptom said the former;
only reading the code found the latter. An in-memory-only catalog mutation is
this codebase's recurring silent failure — the heap-resync quadruple is the tell,
and its absence beside a `tbl.AddNotNull` call is a bug signature worth grepping
for at the other five hand-inlined sites (§21.6).

Gates: `go build ./...`; `go test ./internal/{executor,catalog}/`;
`go test -run 'TestPort_.*(NotNull|Constraint|Inherit)' ./internal/testport/` (19 tests);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (**Q12=2, Q13=35** — Rule #1).

## 23. M0134-0005p — NOT NULL inheritance bookkeeping (`coninhcount`/`conislocal`/`convalidated`) drifted from PG (LANDED 2026-08-18)

### 23.1 Why this slice

Census re-measured at HEAD `09a53e8f` with the §1 command (probe
`tmp/ralph-handoffs/m0134-0005p-nninh/census`): **775 lines / 29 hunks — an exact
match to the 0005o baseline**, so no unmasking occurred and §21.1's ranking held.
This slice takes the cluster's largest *silent* sub-bug: the fixture's
`get_nnconstraint_info` probe (`SELECT conrelid::regclass, conname, convalidated,
conislocal, coninhcount FROM pg_constraint …`, `constraints.sql:876`, 8 call
sites) reported wrong counter values under multi-level `INHERITS` and under
partitioning — a wrong answer, not an error.

**The research pass again changed the slice boundary**, as §21.1's rule predicts.
Two corrections worth keeping:

- The ~200-line hunk span is **not** ~200 lines of this bug. Those 5 hunks are
  *entangled* with at least three independently-rooted divergences (NOTICE
  suppression, a false "inherited from more than once" error, a `regclass`
  `ORDER BY` sort bug). Only ~40-60 of the 205 lines were ever ours. **Hunk span
  is an upper bound on a sub-bug's size, never an estimate of it** — a partial
  shrink was the predicted success criterion, and it is what we got.
- `pg_get_partition_constraintdef` — ranked #2 filler by the 0005o baton — was
  **dropped**: it has zero occurrences in the current `constraints.sql`/`.out`.
  The `:912` line the baton cited is actually the diamond-inheritance block, i.e.
  part of *this* bug. It remains real feature debt (a `pg_proc` stub at
  `internal/initdb/pg_proc_seed_data.go:2272`, OID 3408, with no executor
  implementation) but witnesses no hunk; it is ledgered, not scheduled.

### 23.2 The PG invariant

From `pg_constraint.c:742 AdjustNotNullInheritance`,
`tablecmds.c:7913 ATExecSetNotNull`, `heap.c:2385 AddRelationNewConstraints`:

- **`coninhcount`** counts *immediate* parent edges enforcing the constraint,
  incremented **once per distinct parent-child edge**. PG's recursion carries
  **no visited-set at all** — it relies on the inheritance DAG being acyclic
  (enforced at `INHERIT`/`ATTACH` time), so a diamond descendant reached via two
  parent edges is legitimately counted **twice**.
- **`conislocal`** is true iff the constraint was ALSO declared directly on this
  table — independent of `coninhcount`; a constraint can be both.
- **`convalidated`** reflects **this table's own** rows, never an ancestor's.
  A fresh/empty table (including a new `PARTITION OF` child) always cooks
  `convalidated=true`.

`AdjustNotNullInheritance` returns a bool "an existing constraint absorbed this";
the caller creates a new row only when it returns false. That return value is the
structural distinction goopg was missing.

### 23.3 The four defects (all `internal/executor/operators_ddl.go`)

The root cause is shared: `catalog.Table.AddNotNull`
(`internal/catalog/catalog.go:253-258`) is a **pure append that validates
nothing** — every call site decides `NoInherit`/`NotValid`/`IsLocal`/`InhCount`
by hand. Five independent hand-written call sites, no shared "merge-or-create"
primitive, is exactly how four different arithmetic errors accumulated.

| # | site | defect | fix |
|---|---|---|---|
| A | `:9858` `AlterTableSetNotNull` | the already-exists branch fell through to create **and cascade** | merge in place (`IsLocal=true`, else validate a local NOT VALID) and **return without recursing** — PG's existing-constraint branch (`tablecmds.c:7950-8010`) never recurses; only `:8012-8082` does |
| B1 | `:11105` `cascadeNotNullToChildrenAt` | `notValid` hardcoded `false` | thread the source constraint's `NotValid` through the helper (all 3 callers updated: SET NOT NULL→`false`, ADD CONSTRAINT→`act.NotValid`, PK-synthesis→`false`) |
| B2 | `:11068-11109` same | `visited` keyed on child OID alone → a diamond descendant counted once instead of once per edge | re-key to the **(child OID, immediate-parent OID) edge**; `maxNotNullCascadeDepth` stays as the cycle backstop |
| C | `:4919` `execCreatePartitionChild` | copied `parentNC.NotValid` onto a brand-new empty partition | pass `notValid=false` unconditionally (§23.2) |

**Not a heap-resync bug.** Unlike 0005o, all four sites already call the
`catalogHeapSyncAvailable`→…→`syncTableToCatalogHeap` quadruple. This was pure
bookkeeping arithmetic — a distinct failure mode, and the reason the guards for
it can be in-memory (only C's needed a full-server `pg_constraint` read).

### 23.4 Out of scope (each separately ledgered 2026-08-18)

`ALTER TABLE … ATTACH PARTITION` absorption (write-site **D**) is a **complete
no-op** on NOT NULL bookkeeping — `markAttachedColumnsInherited`
(`:13215-13227`) only flips `Columns[i].Inherited`. It was cut from this slice
for the §21.1 reason: its PG oracle could not be pinned to a function inside
`ATExecAttachPartition` during the research pass, and the milestone's rule is to
size from pinned code. It is structurally "A's already-exists branch at ATTACH
time, but `conislocal` flips the OPPOSITE direction (t→f)". Also deferred: the
recursive validation of descendants (`QueueNNConstraintValidation`,
`tablecmds.c:13213-13290`) that PG's `ATExecValidateConstraint` performs —
**discovered by this slice and made visible by its own correct B1 fix**; the
`regclass` `ORDER BY` sort bug; the suppressed inheritance NOTICEs; the false
"inherited from more than once" error; and the diff:616-621 check-ordering bug
(D1 family, §21.3).

### 23.5 Outcome

`constraints` regress diff **775 → 763 lines** (−12) at 29 → 30 hunks (a hunk
*split* from the line shift, not a new divergence). A partial shrink was the
predicted and correct result per §23.1.

Guards: `internal/testport/notnull_inherit_counters_test.go` (4 new tests, each
verified FAIL-before/PASS-after by reverting only `operators_ddl.go`) — diamond
`coninhcount`=2; repeat SET NOT NULL does not double-bump and validates a local
NOT VALID; NOT VALID propagates to a cascaded child; a fresh `PARTITION OF` child
is always validated (full-server `pg_constraint` read).

Gates: `go build ./...`; `go test ./internal/{executor,catalog}/`;
`go test -run 'TestPort_.*(NotNull|Constraint|Inherit|Partition)' ./internal/testport/`
(40 subtests); `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (**Q12=2, Q13=35** — Rule #1).

## 24. M0134-0005q — `ATTACH`/`DETACH PARTITION` never absorb the child's NOT NULL constraints

Status: **accepted / LANDED 2026-08-18**. Baseline at selection: **763 diff lines / 30 hunks**
(re-measured at `bae52414`, unchanged from §23.5 — 0005p unmasked nothing).

### 24.1 Symptom (what the regress diff witnesses)

Two hunks, ~54 diff lines total:

| hunk | source | symptom |
|---|---|---|
| `@@ -1468,54 +1466,49 @@` | `constraints.sql:918-960` | `notnull_tbl1_2`/`_3` keep `conislocal=t, coninhcount=0` after `ATTACH PARTITION` (PG: `f`/`1`); the follow-on `SET NOT NULL` / `VALIDATE CONSTRAINT nn3` errors PG raises never fire; and `ALTER TABLE pp_nn ATTACH PARTITION pp_nn_1` silently succeeds where PG raises `42P17 constraint "nn1" conflicts with NOT VALID constraint on child table "pp_nn_1"`, desynchronising ~12 further lines |
| `@@ -1545,18 +1538,17 @@` | `constraints.sql:983-991` | the `notnull_part1_*_upg` pg_upgrade fixture — same `t/0` vs `f/1` divergence, **4 lines only** |

**Not this bug, inside the same spans** (do not credit to this slice): the
`regclass` `ORDER BY` sort bug and the suppressed `merging column` NOTICE (both
ledgered under the 0005p rows), the recursive `QueueNNConstraintValidation` gap
(ledgered under 0005p), and the ATACC3/`ALTER TABLE … INHERIT` validation hunks
(0005k) — the latter share PG's fan-in function but arrive from a **different
call site**.

### 24.2 Root cause — PG's fan-in function has no goopg counterpart

The 0005p ledger guessed `AdjustNotNullInheritance`; **that guess is wrong for
this path**. `AdjustNotNullInheritance` serves only same-table paths (`ADD
CONSTRAINT` / `ADD PRIMARY KEY` / identity — 0005n/0005o). ATTACH flows through:

- `tablecmds.c:ATExecAttachPartition` (:20250) → `CreateInheritance(attachrel,
  rel, ispartition=true)` (:20448)
- `CreateInheritance` (:17374) → `MergeAttributesIntoExisting` (:17419, the
  column-level twin goopg *does* implement as `markAttachedColumnsInherited`)
  → **`MergeConstraintsIntoExisting` (:17422)** — the constraint-level merge,
  **entirely absent in goopg**.

`MergeConstraintsIntoExisting` (:17638-17817), per parent constraint with
`contype IN (CHECK, NOTNULL)` and `!connoinherit`, matching the child's NOT NULL
**by attribute number, not by name** (:17709-17718):

1. child `connoinherit` → `42P17` `constraint "%s" conflicts with non-inherited constraint on child table "%s"` (:17736)
2. parent validated && child enforced && !child validated → `42P17` `constraint "%s" conflicts with NOT VALID constraint on child table "%s"` (:17746) ← the missing `pp_nn` error
3. parent enforced && !child enforced → `42P17` NOT ENFORCED conflict (:17758) — **goopg's model has no `conenforced` on NOT NULL; out of scope, ledgered**
4. otherwise absorb: `coninhcount++` (:17771); **only when the parent is `RELKIND_PARTITIONED_TABLE`** `conislocal = false` (:17782) — plain `INHERITS` leaves `conislocal` alone
5. no matching child constraint → `42804` `column "%s" in child table "%s" must be marked NOT NULL` (:17797)

DETACH is the exact twin: `ATExecDetachPartition`/`…Finalize` → `RemoveInheritance`
(:17950-18144, shared with `ATExecDropInherit`): `coninhcount--`, and
`conislocal = true` once it reaches 0 (:18123). Never decrement past 0 (PG
`elog`s an internal assert there, :18119).

The constraint **keeps its own name** across absorb and detach — the merge is
positional, never a rename.

### 24.3 goopg's gap (confirmed, both directions)

- `internal/executor/operators_ddl.go:8332` (`AlterTableAttachPartition`): clones
  FKs, checks default-partition conflicts, propagates indexes, calls
  `markAttachedColumnsInherited` (:8508) — and touches `NotNullConstraints`
  **zero times**.
- `:8730` / `:8763` (`AlterTableDetachPartition`, both branches): call
  `clearAttachedColumnsInherited` (:13273), likewise never touching
  `NotNullConstraints`.
- The column-level pair `markAttachedColumnsInherited` (:13250) /
  `clearAttachedColumnsInherited` (:13273) is the **structural template** — this
  slice adds their constraint-level twins.
- `catalog.Table.AddNotNull` (`internal/catalog/catalog.go:254`) is a pure append
  that validates nothing (the 0005p root cause); the in-place-merge shape to
  reuse is `AlterTableSetNotNull`'s already-exists branch
  (`operators_ddl.go:9862-9882`).
- **Heap resync**: per the 0005o convention every `NotNullConstraints` write site
  needs `catalogHeapSyncAvailable` → `MaterializeWriterXID` →
  `deleteCatalogRowsForOID` (looped over `tableCatalogDBOids`) →
  `syncTableToCatalogHeap` (template at `:9891-9901`). ATTACH/DETACH run none of
  it today. PG writes rows only on `conrelid = attachrel`, so **only the child**
  needs the resync — the parent's own constraint rows are untouched.

### 24.4 Design

Add the two constraint-level twins alongside the existing column-level ones, and
run the merge at **statement execution time** in every ATTACH branch (including
the deferred one): PG raises its `42P17`/`42804` errors from
`ATExecAttachPartition` itself, so deferring the check to COMMIT would report the
error at the wrong statement.

1. `mergeNotNullOnAttach(parent, child *catalog.Table) error` — cases 1, 2, 4, 5
   of §24.2 (case 3 omitted, ledgered). Match on `ColName` case-folded (goopg's
   proxy for PG's attnum match — equivalent because a column name is unique
   within a table).
2. `unmergeNotNullOnDetach(parent, child *catalog.Table)` — `InhCount--`,
   `IsLocal = (InhCount == 0)`, clamped at 0.
3. Heap-resync quadruple for the child after either mutation.
4. Error messages byte-identical to PG's, with PG's SQLSTATEs (`42P17`, `42804`).

### 24.5 Out of scope (ledgered, not fixed here)

- The **CHECK-constraint half** of `MergeConstraintsIntoExisting` — same function
  upstream, separate witness set.
- The **NOT ENFORCED** conflict branch — goopg's `NamedNotNullConstraint` has no
  `conenforced` field; unwitnessed by this fixture.
- The plain `ALTER TABLE … INHERIT` call site (`ATExecAddInherit`), which shares
  the upstream function but differs in the `conislocal` rule (§24.2 step 4) —
  that is 0005k's territory.

### 24.6 Outcome (LANDED 2026-08-18)

Measured **763 → 738 lines**, hunks 30 → **31** — the +1 is a pure split of the
shrunk hunk (the surrounding `@@` header list is byte-identical). The §24.1 sizing
prediction held exactly: ~54 lines were ever this bug's, and the rest of the span
belongs to the separately-ledgered `regclass` sort / suppressed NOTICE /
`QueueNNConstraintValidation` gaps, which were correctly not chased.

Landed as `mergeNotNullOnAttach` (`internal/executor/operators_ddl.go:13347`) and
`unmergeNotNullOnDetach` (`:13405`), wired at ATTACH (~:8409), concurrent-detach
phase 2 (~:8757) and plain DETACH/FINALIZE (~:8790), each followed by the
child-only heap-resync quadruple. 144 production lines, 4 new real-server guards in
`internal/testport/notnull_inherit_counters_test.go` (all FAIL-pre / PASS-post).

**One oracle correction worth carrying forward:** §24.2's steps 1 and 2 quote PG's
`errmsg` with `%s` = the constraint name, and the brief assumed that meant the
*parent's*. It is the **child's** (`NameStr(child_con->conname)`), confirmed
against `expected/constraints.out:1581`, where `nn1` is `pp_nn_1`'s own
constraint. **When pinning a message's arguments, read the expected output, not
only the `errmsg` call site** — the call site alone is ambiguous about which of
two same-typed variables is in scope.

Gates: `go build ./...`; `go test ./internal/{executor,catalog}/`;
`go test -run 'TestPort_.*(NotNull|Constraint|Inherit|Partition)' ./internal/testport/`;
`scripts/pg-regress-runner.sh constraints`;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`;
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35 — Rule #1).

## 25. M0134-0005r — plain `ALTER TABLE … INHERIT / NO INHERIT` never merges NOT NULL

**Baseline at entry:** 738 lines / 31 hunks (re-measured 2026-08-18 at `2f1d324d`;
never compare to a pre-2026-08-18 number).

### 25.1 The discovery

M0134-0005q (§24) implemented the *partition* half of NOT NULL constraint
absorption: `mergeNotNullOnAttach` / `unmergeNotNullOnDetach`
(`internal/executor/operators_ddl.go:13357` / `:13413`), wired at `ATTACH` and the
three `DETACH` sites. Upstream, the partition and the plain-inheritance halves are
**the same function**: `CreateInheritance(…, ispartition)`
(`postgres/src/backend/commands/tablecmds.c:17374`) → `MergeConstraintsIntoExisting`
(`:17636`) is reached from `ATExecAttachPartition` *and* from `ATExecAddInherit`
(`:17261`, `ispartition=false`). goopg had only wired the partition caller.

goopg's plain-inheritance paths therefore do **no** NOT NULL bookkeeping at all:

- `parser.AlterTableInherit` (`operators_ddl.go:9234-9296`) copies missing *columns*
  only; the comment at `:9286` admits the constraint half is unimplemented ("v0").
- `parser.AlterTableNoInherit` (`:9297-9341`) unregisters the inheritance edge and
  clears `col.Inherited`, but never decrements the NOT NULL constraint's `InhCount`.

Consequence in `constraints.sql`: `ALTER TABLE … INHERIT` leaves the child's NOT NULL
`InhCount` at 0 / `IsLocal` true, so later `\d+` and `pg_constraint` probes diverge,
and the conflict checks PG raises at merge time (NO INHERIT status change, NOT VALID
conflict, NOT ENFORCED conflict, parent constraint missing on child) never fire.

### 25.2 The one behavioural delta vs the partition half

`MergeConstraintsIntoExisting` branches on `is_partition` in exactly one place —
`tablecmds.c:17771-17777`: `conislocal` is cleared to false **only** when the parent
is a partitioned table. For plain `INHERITS`, the child's constraint keeps
`conislocal = true` while `coninhcount` is incremented; the constraint is then both
local and inherited, and survives a later `NO INHERIT` as a local constraint.

`RemoveInheritance` (`:17950`, decrement loop at `:18103-18125`) has **no**
`is_partition` branch — the detach-side helper is reusable unchanged.

### 25.3 Message-argument sources (pinned, not guessed)

Per §24's lesson, arguments are pinned from the oracle source and, where witnessed,
from `postgres/src/test/regress/expected/constraints.out`:

- NO-INHERIT / NOT-VALID / NOT-ENFORCED conflicts (`tablecmds.c:17736-17762`) name
  the **child's** constraint name.
- The "missing constraint on child" error (`:17797-17812`) names the **parent's**
  attname.

### 25.4 The slice

Parameterize `mergeNotNullOnAttach(parent, child *catalog.Table, isPartition bool)`;
gate the `IsLocal = false` assignment (`:13399`) on `isPartition`. Wire it into
`AlterTableInherit` with `isPartition=false` and wire the unchanged
`unmergeNotNullOnDetach` into `AlterTableNoInherit`. Update the existing ATTACH /
DETACH call sites to pass `isPartition=true`. **Twin pair:** INHERIT ↔ NO INHERIT
must land together (the §24 precedent — a merge without its unmerge leaves
`InhCount` monotonically climbing across INHERIT/NO INHERIT cycles).

**Measured result: 738/31 → 731 lines / 32 hunks** (predicted ~715-724/31 — the
shrink landed 7 lines short of the band, and hunk count rose by one; see §25.6
for the root cause, which is a newly *unmasked* pre-existing bug, not a
regression). The plain-
inheritance NOT NULL cluster owns only ~45-55 of the 738 lines and splits three ways;
this slice takes cluster A (the missing call, ~14-18 lines). Deliberately out of
scope, carried to the ledger:

- **Cluster B (~26 lines)** — `CREATE TABLE … INHERITS` merge-time NOT NULL name /
  `conislocal` handling (`operators_ddl.go:3966-4051`) is a *different, pre-existing*
  bug, not a missing call: the witnessed case is a child column redeclared **without**
  its own `NOT NULL` keyword, which the code's "verified live against PG 18.3"
  comment (`:4032`) does not actually cover.
- **Cluster C (~4 lines)** — the single-parent redeclare case emits no "merging
  column" NOTICE; goopg's only NOTICE site (`:1831`) is the multi-parent dedup path.

### 25.5 The deferred-to-COMMIT twin (round 2)

`ALTER TABLE … {NO} INHERIT` has **two** execution paths in goopg, and only wiring
the immediate one would have left the bug unfixed under `BEGIN`/`COMMIT`. Inside an
explicit transaction the case records a `PendingInheritanceChange` and `break`s
(M0118-0008 alter-table-4, transactional-DDL visibility: the link must stay invisible
to concurrent sessions until commit); the real catalog mutation happens in
`ApplyPendingInheritanceChanges` (`operators_ddl.go:7341`), which already replayed the
column copy but did no constraint merge. Both arms are now mirrored there —
`unmergeNotNullOnDetach` before `UnregisterInheritanceChild`, and
`mergeNotNullOnAttach(…, false)` after the column copy with `RegisterInheritanceChild`
moved after it, matching the immediate path's ordering.

`ApplyPendingInheritanceChanges` has no error-return channel and runs before
`TxnMgr.Commit`, so the merge error is deliberately discarded there; see §25.6. Heap
resync is deliberately omitted, matching the sibling deferred function
`ApplyPendingPartitionAttaches`, which omits it too.

### 25.6 Carried out of this slice (all ledgered)

- **The `LINE N:` / `^` echo quirk** — the `mergeNotNullOnAttach` call sites stamp
  `ee.Pos = act.Pos()`, so goopg annotates these `42P17`/`42804` errors with a cursor
  position PG does not set for them. Pre-existing (already present at the
  ATTACH-PARTITION baseline), but this slice *unmasks* it at the new INHERIT sites —
  which is why the diff shrank only to 731 and gained a hunk instead of hitting the
  predicted 715-724/31.
- **No statement-time conflict check on the deferred path** — PG's `ATExecAddInherit`
  raises `42P17`/`42804` synchronously regardless of transaction wrapping; goopg's
  deferred path performs the merge only at COMMIT, so a genuine conflict is silently
  skipped rather than raised at the `ALTER TABLE`. Unwitnessed by `constraints.sql`
  (autocommit-only fixtures).
- **Cluster B** (`CREATE TABLE … INHERITS` merge-time NOT NULL name / `conislocal`)
  and **cluster C** (missing single-parent "merging column" NOTICE), per §25.4.
- `ALTER TABLE … ADD CONSTRAINT` cascading down the inheritance tree (the
  `ditto`/`ATACC2` case, diff ~317/321) — same missing-merge-check family, different
  call site.

## 26. M0134-0005s — `CREATE TABLE … INHERITS` never marks a locally-sourced NOT NULL as `conislocal`

Baseline when this slice was selected: **731 diff lines / 32 hunks** (HEAD
`d70c2e0b`, re-measured 2026-08-18 — never compare against an older number).
Census: `tmp/ralph-handoffs/m0134-0005s-census/report.md`.

### 26.1 The divergence

`postgres/src/test/regress/sql/constraints.sql:789-790`:

```sql
CREATE TABLE notnull_tbl4_cld2 (PRIMARY KEY (a) DEFERRABLE) INHERITS (notnull_tbl4);
CREATE TABLE notnull_tbl4_cld3 (PRIMARY KEY (a) DEFERRABLE, CONSTRAINT a_nn NOT NULL a)
  INHERITS (notnull_tbl4);
```

goopg's `\d+` reports the child's NOT NULL as purely `(inherited)`, and for
`cld2` under the *parent's* constraint name:

```
-  "notnull_tbl4_cld2_a_not_null" NOT NULL "a" (local, inherited)   <- PG
+  "notnull_tbl4_a_not_null"      NOT NULL "a" (inherited)          <- goopg
-  "a_nn" NOT NULL "a" (local, inherited)                           <- PG
+  "a_nn" NOT NULL "a" (inherited)                                  <- goopg
```

Both children declare a NOT NULL of their own — `cld2` implicitly via
`PRIMARY KEY (a)`, `cld3` explicitly via `CONSTRAINT a_nn NOT NULL a` — *and*
inherit one from `notnull_tbl4`. PG's answer is that the constraint is both
local and inherited; goopg's is that it is only inherited.

### 26.2 The PG oracle

`postgres/src/backend/catalog/heap.c:2897` `AddRelationNotNullConstraints`,
called from `DefineRelation` (`tablecmds.c:1354`), takes two lists:

- `constraints` — every NOT NULL the **child's own body** produced: explicit
  column `NOT NULL`, table-level `CONSTRAINT n NOT NULL c`, **and the
  PK-implied one**, all duplicate-merged at `heap.c:2955-2998`.
- `old_notnulls` — the parent-inherited `CookedConstraint`s that
  `MergeAttributes` (`tablecmds.c:2546`) carried down, built from every direct
  parent independently of whether the child redeclares the column.

The two loops that follow are the whole rule:

- `heap.c:3038-3050` — for **every** entry in `constraints`, PG calls
  `StoreRelNotNull(rel, conname, attnum, /*islocal=*/true, …, inhcount, …)`.
  `islocal` is **unconditionally true**; `inhcount` counts however many
  `old_notnulls` entries were absorbed (`heap.c:3009-3018`). This is the
  `(local, inherited)` case. The name comes from `ChooseConstraintName` over
  `RelationGetRelationName(rel)` — the **child's** name, not the parent's.
- `heap.c:3057-3120` — only for columns with **no** entry in `constraints` at
  all does PG store the parent's name with `islocal=false`. This is the pure
  `(inherited)` case.

So: a local source always wins both the flag and the name; inheritance only
adds to `coninhcount`.

### 26.3 goopg's divergence

`internal/executor/operators_ddl.go:3966-4062`, `CreateTable`'s per-column
NOT-NULL merge loop. `entries` (built at `:3978-3995` from the column's own
local sources — `NotNullExplicit`, the `pkColSet` PK-implied branch, LIKE
copies) is exactly PG's `constraints` list. But `isLocal` and `name` are
decided earlier, at `:4012-4051`, purely from `col.Inherited` — i.e. from
whether the column arrived via a bare `INHERITS` copy — and are **never
revisited** by the `if len(entries) > 0` block at `:4052-4062`:

```go
if len(entries) > 0 {
    mergedName, mergedNoInherit, execErr := mergeNotNullEntries(col.Name, entries)
    …
    if mergedName != "" { name = mergedName }
    noInherit = mergedNoInherit
}
tbl.AddNotNull(name, col.Name, im.AllocOID(), noInherit, false, isLocal, inhCount)
```

For `notnull_tbl4_cld2`, `a` never appears in `s.Columns` (only in the
table-level `PRIMARY KEY (a)`), so `col.Inherited=true` was stamped at
`:3267`; the `col.Inherited` branch set `isLocal=false` and `name` = the
parent's constraint name; and although `entries` is non-empty (the PK-implied
`nnEntry{}` from `:3985-3987`), `mergedName` is `""` for an unnamed entry, so
neither the flag nor the name is corrected.

### 26.4 The fix

Inside the `len(entries) > 0` block, mirror `heap.c:3038-3050`:

1. `isLocal = true` — a column with any local NOT-NULL source is `conislocal`
   regardless of what it also inherited. `inhCount` is unchanged (it already
   counts the absorbed parent constraints, so the result is
   `(local, inherited)`).
2. When `mergedName == ""` (no explicit name anywhere in `entries`), recompute
   `name` from the **child's** relation name — `<child>_<col>_not_null` — via
   the same auto-naming helper the non-inherited path already uses, instead of
   leaving the parent's name that the `col.Inherited` branch assigned. An
   explicit name in `entries` (`cld3`'s `a_nn`) still wins, as it does today.

### 26.5 Rule #2 twins — already correct, do not touch

The statement-time twin `mergeNotNullOnAttach`
(`operators_ddl.go:13422-13471`, reached from both the ATTACH PARTITION site
`:8444` and the plain INHERIT site `:9332`) and its inverse
`unmergeNotNullOnDetach` (`:13481-13501`) already implement this rule — they
were fixed under M0134-0005q/M0134-0005r and never clear a pre-existing local
flag. Only the CREATE-TABLE-time path has the gap. The deferred-to-COMMIT
`ALTER TABLE … INHERIT` variant routes through the *same*
`mergeNotNullOnAttach` call site (`PendingInheritanceChange`, `:9296-9302`),
so it carries no separate merge logic and needs no separate change.

### 26.6 Explicitly out of scope (verify, do not fold in)

- **`ATACC3 (PRIMARY KEY (a)) INHERITS (ATACC1)`** where `ATACC1`'s own
  not-null is `NO INHERIT` (diff `:290-301`, 4 lines). Same symptom family,
  but goopg drops the not-null obligation *entirely* rather than mislabelling
  it, because of the separate NO-INHERIT clearing of `col.NotNull` at
  `operators_ddl.go:1854-1861`. Re-probe after the primary fix; if it is not
  cleared, it is its own slice.
- **`notnull_tbl5_child` after `ALTER TABLE ONLY notnull_tbl5 DROP CONSTRAINT
  ann`** (diff `:486-487`). Same `(inherited)` tag, different root cause:
  `DROP CONSTRAINT … ONLY` does not decrement the child's `InhCount` /
  restore `IsLocal`. Separate fix site.

### 26.7 Expected payoff

4 diff lines guaranteed (`notnull_tbl4_cld2`, `notnull_tbl4_cld3`), i.e.
**731 → ~727 lines**, with the ATACC3 case worth 4 more if it shares the gap.
The census's original "~26 lines" estimate for this cluster over-counted: it
swept in unrelated neighbours such as the `notnull_tbl2_a_seq` sequence-owner
schema-qualification diff at `:278-283`.

### 26.8 Measured outcome (LANDED 2026-08-18, M0134-0005s)

`internal/executor/operators_ddl.go`: `childAutoName` is captured at `:3997`
*before* the `col.Inherited` branch can overwrite `name` with the parent's, and
the `len(entries) > 0` block (`:4056-4077`) now sets `isLocal = true`
unconditionally and falls back to `childAutoName` when `mergedName == ""`.
`inhCount` is untouched. Guard tests:
`internal/executor/operators_ddl_createinh_notnull_test.go` (PK-implied and
explicit-name cases; both verified FAIL-pre / PASS-post via a `git stash` A/B).

**731 lines / 32 hunks → 707 lines / 31 hunks.** The drop is 24 lines, not the
predicted 4, because once `notnull_tbl4_cld2`/`cld3` match PG byte-for-byte the
diff tool drops the entire hunk — context lines included — rather than only the
two changed marker lines. Both named cases are fully cleared. This is worth
remembering when estimating future slices from this file: a hunk that goes
fully clean is worth its context, so per-slice payoff is systematically
under-predicted by marker-line counting.

Gates: `go build ./...`; `go test ./internal/{executor,catalog}/`;
`go test -run 'TestPort_.*(NotNull|Constraint|Inherit|Partition)' ./internal/testport/`
(75.5s PASS); `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
(`internal/initdb` ran cold at 438s — cache invalidation from the diff, not a
regression); `scripts/tpch-spotcheck.sh` PASS (**Q12=2, Q13=35**, Rule #1);
pgbench smoke via the pre-commit hook.

### 26.9 ATACC3 re-probe (§26.6) — root cause now pinned, still deferred

The `ATACC3 (PRIMARY KEY (a)) INHERITS (ATACC1)` case is **not** cleared, but the
fix changed its shape and pinned the cause. Pre-fix it had both a wrong name and
a wrong tag; post-fix the name is correct (a side effect of §26.4's
`childAutoName` half) and only the tag is wrong: goopg reports
`(local, inherited)` where PG expects **no tag at all**. Root cause is a
pre-existing, untouched loop at `operators_ddl.go:4012-4023` that counts a
parent's NOT NULL toward `inhCount` **even when that parent's constraint is
`NO INHERIT`** — PG's `MergeAttributes` never places a `NO INHERIT` parent
constraint into `old_notnulls` at all, so the child inherits nothing and
`coninhcount` stays 0. This is one bug, not the two-part entanglement §26.6
guessed at: the `col.NotNull` clearing at `:1854-1861` is the *correct* half.
Ledgered with this resume point; a good ~4-line follow-up slice.

## 27. M0134-0005t — a `NO INHERIT` parent NOT NULL was counted toward the child's `coninhcount`

Closes the deferral §26.9 opened, and with it the last of §26.6's carve-out.

### 27.1 The divergence

`constraints.sql`'s `CREATE TABLE ATACC3 (PRIMARY KEY (a)) INHERITS (ATACC1)`
(diff `:290-301`), where ATACC1's own not-null is `NO INHERIT`: goopg tagged the
child's PK-implied constraint `(local, inherited)`; PG 18.3 emits **no tag** —
the constraint is purely local, `coninhcount = 0`.

### 27.2 The PG oracle (re-verified, not inherited from §26.9's note)

`MergeAttributes` never sees a `NO INHERIT` parent not-null at all. It collects
the parent's constraints via

```c
/* tablecmds.c:2757 */
nnconstrs = RelationGetNotNullConstraints(RelationGetRelid(relation),
                                          true, /* include_noinh = */ false);
```

and `RelationGetNotNullConstraints` (`catalog/pg_constraint.c:834`) filters at
the top of its scan:

```c
if (conForm->connoinherit && !include_noinh)
    continue;
```

That single list feeds **both** consumers: `nncols` (the `attnotnull` request
set, built at `:2759-2760`) and the `nnconstraints` list appended at
`tablecmds.c:2952`. So a `NO INHERIT` parent constraint contributes neither an
inherited count nor a name — it is invisible to inheritance, full stop.

### 27.3 The fix

Three `if …NoInherit { continue }` guards, 9 lines total, in
`internal/executor/operators_ddl.go`:

1. `execCreateTable`'s `col.Inherited && s.PartitionOf == nil` parent-scan loop
   (`:4013`) — the one that both increments `inhCount` and may assign
   `name = pnc.Name`. Guarding before the `EqualFold` match kills both effects at
   once, which is why the name half needed no separate change.
2. The sibling `!col.Inherited && s.PartitionOf == nil` loop (`:4043`) that sets
   `inhCount = 1` for a child re-declaring the column in its own list. Same
   oracle, same filter — Rule #2 demanded both loops move together.
3. `unmergeNotNullOnDetach` (`:13504`), which selected parent constraints
   *without* the `NoInherit` skip its ATTACH twin `mergeNotNullOnAttach` already
   had. Behaviourally a no-op today (a `NO INHERIT` parent constraint was never
   absorbed, so the `InhCount > 0` clamp already swallowed the decrement) — this
   closes the latent Rule-#2 twin divergence ledgered under M0134-0005q rather
   than leaving a future CHECK/multi-parent extension to make it live.

`:1854-1861`'s `col.NotNull` clearing is the correct half and was **not**
touched, per §26.9.

### 27.4 Measured outcome (LANDED 2026-08-18)

`constraints` regress diff **707 → 702 lines**, hunks unchanged at **31** — the
~4-line estimate in §26.9's ledger row, met (5). The ATACC3 hunk's
`(local, inherited)` mis-tag is gone and goopg now matches PG byte-for-byte on
that line.

Guard: `TestPort_NotNullNoInheritParentSkippedOnCreateInherits`
(`internal/testport/notnull_inherit_counters_test.go`), asserting all three
properties the oracle implies — `coninhcount = 0`, `conislocal = true`, and a
name that is *not* the parent's. FAIL pre (`coninhcount = "1", want 0`) / PASS
post, verified by stashing the executor edits in-session.

Gates: `go build ./...`; `go test ./internal/{executor,catalog}/`;
`go test -run 'TestPort_.*(NotNull|Constraint|Inherit|Partition)'
./internal/testport/` (75.3s PASS, 12 pre-existing guards + the new one);
`scripts/pg-regress-runner.sh constraints` (702/31);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
(`internal/initdb` cold at 436s — diff cache invalidation, not a regression;
note this scope EXCLUDES `internal/testport`); `scripts/tpch-spotcheck.sh` PASS
(**Q12=2, Q13=35**, Rule #1); pgbench smoke via the pre-commit hook.

### 27.5 Carried out of this slice (ledgered)

One residual line remains in the ATACC3 hunk, **pre-existing and unchanged by
this diff**: `\d+`'s per-column *Nullable* display shows blank where PG shows
`not null`, for a column that is both `col.Inherited` (plain INHERITS) and
PK-implied NOT NULL. The describe renderer evidently reads a different signal
than `catalog.Table.Columns[i].NotNull` / the `pg_constraint` row — a separate
bug in the describe path, not in the inheritance bookkeeping this section fixes.

## 28. M0134-0005u — goopg stamped a cursor position PG does not set on the NOT-NULL merge errors

Baseline when this slice was selected: **702 diff lines / 31 hunks** (HEAD
`26b6b5ac`, per §27 — never compare against an older number). Research pass:
`tmp/ralph-handoffs/m0134-0005u-eepos-audit/report.md`.

### 28.1 The divergence

Both `mergeNotNullOnAttach` call sites wrapped their error return in

```go
if ee, ok := err.(*ExecError); ok && ee.Pos == 0 { ee.Pos = act.Pos() }
```

so goopg emitted a `FieldPosition` — rendered by psql as a `LINE N:` / `^`
caret block — on the `42P17` / `42804` NOT-NULL merge conflicts raised by
`ALTER TABLE … ATTACH PARTITION` and plain `ALTER TABLE … INHERIT`. PG 18.3
raises both from `MergeConstraintsIntoExisting`
(`postgres/src/backend/commands/tablecmds.c:17638-17817`), which calls no
`errposition()`; `postgres/src/test/regress/expected/constraints.out:1497,1581`
accordingly carries no caret block. This is the §25.6 quirk, ledgered under
M0134-0005r and re-measured under M0134-0005s.

Note the gate asymmetry that makes this visible at all: `scripts/pg-regress-runner.sh`
uses its own bash `normalise_output` (script lines ~250-266), which does **not**
strip `LINE `/`^`, whereas `NormalizeRegressOutput`
(`internal/testport/framework/regress.go`) does. A payoff claim about error
positions is meaningless until you name which normaliser it is about.

### 28.2 The audit — the ledger's "36 lines across ~20 sites" was wrong

The M0134-0005s ledger row scoped this as "a single audit of all ~20
`ee.Pos == 0` stamping sites … worth 36 lines". The research pass enumerated all
20 sites in `internal/executor/operators_ddl.go` and found that number does not
survive contact:

- **15 sites** are lock-acquire / deadlock / cancel paths (`acquireDDLLockTxn`
  and friends, plus `detachPartitionFKRefCheck`). PG never attaches a position to
  these either (`lock.c`, `proc.c`, `deadlock.c` call no `errposition`), so
  removing the stamps *would* be PG-faithful — but none are witnessed by
  `constraints.sql`, which is serial and cannot reach a lock wait. Payoff on this
  gate: **zero**. Risk: non-zero, because the isolation specs (`alter-table-*`,
  `detach-partition-concurrently-*`) do exercise them and were not checked.
- **1 site** (`:11600`, the FK phase-3 `validateFKConstraintExistingRows` scan,
  23503) is a real instance of the same bug — PG's `ri_triggers.c`
  `ri_ReportViolation` sets no position — but it is witnessed by
  `foreign_key.sql`, not `constraints.sql`, and its sibling at `:8196` already
  skips the wrap. It belongs to a `foreign_key`-scoped slice.
- **2 sites** (`:8466` ATTACH, `:9354` INHERIT) are both PG-cited and
  byte-witnessed in `constraints.out`. Those are this slice.

The lesson generalises past this file: an `ee.Pos` site count is not a payoff
estimate. Only sites whose error is *reached by the fixture under measurement*
move the number, and "PG-faithful to remove" and "safe to remove blind" are
different predicates — M0129-S10 broke 26 regress cases by treating a
position-stripping change as site-blind (2026-08-10 ledger row).

### 28.3 What landed

Six lines in `internal/executor/operators_ddl.go`: the two stamping wrappers
replaced by a comment citing `tablecmds.c:17638-17817`. The third
`mergeNotNullOnAttach` call site (`~:7416`, the deferred-to-commit path of §25.5)
already discards its error and carried no stamp.

**Measured result: 702/31 → 672 lines / 30 hunks** — predicted −6/0, actual
−30/−1. The four directly-removed diff lines are real; the rest is unified-diff
realignment, two previously separate hunks merging once the caret lines
disappeared (consistent with the hunk count falling by exactly one). Verified
non-vacuous: `grep 'conflicts with NOT VALID constraint on child table'` over the
new diff returns zero matches — both target errors now match PG byte-for-byte —
and all 30 surviving hunks are pre-existing, unrelated divergences. **This is the
inverse of §27's marker-line rule:** a hunk that goes clean can take far more than
its own line count with it, in either direction. Predict the *sign*, not the
magnitude, and re-measure.

### 28.4 Carried out of this slice (ledgered)

- The 15 lock-acquire `ee.Pos` sites — PG-faithful to remove, zero payoff here,
  unverified against the isolation specs.
- The FK phase-3 site (`:11600`) — same bug, `foreign_key.sql` scope.

## 29. M0134-0005v — `ADD PRIMARY KEY` never verified the pre-existing NOT NULL is VALID and inheritable

### 29.1 Selection — the reachability pass overturned the carried ranking

The §28 baton ranked three candidates. A fixture-reachability pass at HEAD
`fa84e214` (baseline **672 lines / 30 hunks**) retired the top two outright:

- **`ATExecValidateConstraint` descendant recursion** (`postgres/src/backend/commands/tablecmds.c:13219-13300`,
  `QueueNNConstraintValidation`) — every `VALIDATE CONSTRAINT` in the fixture
  (`constraints.sql:864,866,904,934,947`) targets a table with **zero descendants
  at that moment**. The apparent counter-example at `:904` inverts: the preceding
  `ALTER TABLE notnull_tbl1 INHERIT notnull_chld0` (`:901`) makes `notnull_chld0`
  the *parent*. Payoff **0**.
- **The CHECK half of `MergeConstraintsIntoExisting`** (`tablecmds.c:17638-17817`,
  reached only from `ATAddInherit`) — all five `ALTER TABLE … INHERIT` sites
  (`:710,855,857,897,901`) carry NOT NULL constraints only; no shared-name CHECK
  scenario exists in the fixture. Payoff **0**.
- **`DROP CONSTRAINT … ONLY` child `InhCount`/`IsLocal`** — reachable
  (`constraints.sql:803-806`, `notnull_tbl5_child`) but owns exactly **4 lines in
  1 hunk**.

This is the second consecutive loop in which the reachability pass, not the site
census, decided the slice — see §28.2. **A carried ranking is a hypothesis, not a
queue.** Re-measure it against the current diff before briefing.

### 29.2 The divergence

PG's `verifyNotNullPKCompatible` (`tablecmds.c:9577-9608`) rejects an
`ADD PRIMARY KEY` whose column is already covered by a NOT NULL constraint that is
either `NO INHERIT` or not validated — such a constraint cannot back a primary
key. Both arms raise `55000` (`ERRCODE_OBJECT_NOT_IN_PREREQUISITE_STATE`)
`cannot create primary key on column "%s"`, differing only in the DETAIL's marking
and the HINT (`… ALTER CONSTRAINT … INHERIT` vs `… VALIDATE CONSTRAINT`). Neither
`ereport` carries an `errposition`, so goopg must leave `Pos` at 0 (the §28 rule).
It is called from `ATPrepAddPrimaryKey`'s not-recurse loop (`:9556`), which walks
children as well.

goopg's `execAlterTableAddPrimaryKey` (`internal/executor/operators_ddl.go:10876-10913`)
tests only `alreadyHadNotNull := col.NotNull` and, when true, `continue`s at
`:10890` — it never inspects the matching `NotNullConstraints` entry's
`NoInherit`/`NotValid` flags. Three fixture sites hit this today:
`constraints.sql:834-835` (`notnull_tbl1`, `nn` is `NOT VALID`), `:954-955`
(`pp_nn_1` `NOT VALID` via `ALTER TABLE ONLY pp_nn ADD PRIMARY KEY`), and
`:960-961` (same shape, `NO INHERIT`) — together **~10 diff lines across 2 of the
30 hunks**, 2.5x the retired candidate 3.

### 29.3 Scope boundary

`constraints.sql:838` — `ADD GENERATED ALWAYS AS IDENTITY` over an invalid NOT
NULL — looks adjacent but is a **different** PG check (`incompatible NOT VALID
constraint`, `tablecmds.c:8311`, inside `ATExecSetNotNull`'s identity path), worth
~2 more diff lines. Deliberately out of scope; ledgered, not conflated.

### 29.4 Implementation and measured result

Two helpers in `internal/executor/operators_ddl.go`, plus an `only bool` threaded
into `execAlterTableAddPrimaryKey` from its single call site (`:8096` — the
call-site sweep found exactly one caller, so §29.2's "second caller" risk did not
materialise):

- `verifyNotNullPKCompatible(nc, colName, relName)` — the direct port, both arms
  `Pos: 0`.
- `verifyNotNullPKCompatibleChildren` — the `ALTER TABLE ONLY` arm, mirroring PG's
  `!recurse` branch (`tablecmds.c:9532-9557`). It gathers direct
  `InheritanceChildren` + `PartitionChildren` deduped (the `cascadeNotNullToChildrenAt`
  pattern) and names the **child** in the DETAIL, matching the oracle's expected
  output (`pp_nn_1`, not the parent `pp_nn`).

The check block sits after the `42P16` multiple-PK guard and before
`createBTreeIndex`/the null scan — PG's `ATPrepAddPrimaryKey`-before-`ATExecAddIndex`
ordering, the same ordering trap §21 hit.

**Measured: 672 → 647 lines / 30 hunks** (predicted ~662). The over-delivery is
the `ONLY` branch: §29.2 named three fixture sites and two of them
(`constraints.sql:954-955`, `:960-961`) require the children walk, so the narrow
own-column-only reading of the slice would have closed one site out of three.
**Where the brief's file/line pointer and the design section's fixture list
disagree, the fixture list is the real scope** — the pointer is a starting
address, not a boundary. Hunk count held at 30, as neither hunk fully empties.
Verified non-vacuous: the surviving `cannot create primary key on column` lines in
the diff are context-only (no `-` prefix), and a before/after content comparison
(ignoring `@@` churn) shows only the three named sites changed — no new divergence.

### 29.5 Carried out of this slice (ledgered)

- PG's other `!recurse` error — a child with **no** NOT NULL at all
  (`tablecmds.c:9552-9554`); no fixture reaches it.
- The identity-column `NOT VALID` check (§29.3).
- A latent `execAlterTableDropConstraint` cascade gap: it appears to walk only
  `PartitionChildren`, not plain-`INHERITS` children — masked today because the
  one plain-INHERITS fixture case also uses `ONLY`.

## 30. M0134-0005w — four more spurious `LINE n:` cursor positions on constraint-name / NOT-NULL errors (LANDED 2026-08-18)

### 30.1 Symptom

`scripts/pg-regress-runner.sh constraints` differed on five hunks (@248, @261,
@485 ×2, @546) purely because goopg appended a `LINE 1: …` echo plus a `^` caret
under statements where PG 18.3 emits the bare message alone. Same family as §28
(commit `fa84e214`), different call sites — §28 fixed the merge path
(`mergeNotNullOnAttach`); the sites here are on the `ALTER TABLE … ADD
CONSTRAINT … NOT NULL` and `ADD COLUMN … CONSTRAINT <name> NOT NULL` paths.

### 30.2 PG oracle — these `ereport`s carry no `errposition()`

The implementer read `postgres/src/backend/catalog/pg_constraint.c` end to end:
the file contains **zero** `errposition()` calls, so none of
`AdjustNotNullInheritance`'s three rejections sets a cursor position.

| goopg site (`internal/executor/operators_ddl.go`) | PG oracle | error |
|---|---|---|
| `:10121` | `pg_constraint.c:759-767` | `55000` cannot change NO INHERIT status |
| `:10130` | `pg_constraint.c:770-779` | `55000` incompatible NOT VALID constraint |
| `:10139` | `pg_constraint.c:788-795` | `55000` cannot create not-null constraint |
| `:10557` | `heap.c:2645-2652` (`ConstraintNameIsUsed`, in `AddRelationNewConstraints`) | `42710` constraint … already exists |

All four were verified individually — the brief required leaving alone any site
whose PG counterpart *does* set a position; none did.

### 30.3 The fix

`Pos: act.Pos()` → `Pos: 0` at the four sites, each with the PG citation inline.
12 insertions / 4 deletions. Guard tests:
`internal/executor/operators_ddl_errpos_identity_test.go`, one per site,
asserting `ExecError.Pos == 0` alongside the code (FAIL-pre / PASS-post).

**Measured: 647 → 601 lines / 30 → 28 hunks** against a ~635/25-26 prediction —
a 46-line drop where ~12 was forecast. The estimate counted only the diff lines
that name these errors; each spurious position actually costs **three** lines
(the message, the `LINE 1:` echo, and the caret) and the caret line displaces
context, so a position-only bug is consistently ~3-4× the naive line estimate.
Worth carrying: **`Pos` bugs are the cheapest lines-per-source-edit in this
gate** — 4 edited lines closed 46 diff lines.

### 30.4 Sibling check (Hard-won Rule #2)

`Pos: act.Pos()` in this file went 57 → 53 sites. The `42710 already exists`
message also appears at `:9161`, `:9185`, `:9210` inside `RENAME CONSTRAINT`,
but those route through PG's `rename_constraint_internal`, a different function
that was not verified — deliberately left alone rather than changed for symmetry.

### 30.5 Carried out of this slice (ledgered)

Part B of the brief (the identity-column `NOT VALID` check, PG
`tablecmds.c:8311`, carried from §29.5) is **blocked by a larger gap**:
`ALTER TABLE … ALTER COLUMN … ADD GENERATED … AS IDENTITY` is not implemented at
all. `parser/ddl.go:parseAlterColumnAction` has no `ADD` branch and falls through
to `AlterTableNoOp`; there is no `AlterTableActionKind` for it. Verified live
against a running goopg — the statement **returns success while `attidentity` is
never set**, a silent-no-op correctness gap well beyond a local `NOT VALID`
check. Finishing it needs a new action kind, a parser branch, and a port of
`ATExecAddIdentity` (`tablecmds.c:8239-8326`). Ledgered; not a 2-line fix as
§29.5 assumed.

## 31. M0134-0005x — `ADD CONSTRAINT … {PRIMARY KEY|UNIQUE} USING INDEX` was a silent no-op (LANDED 2026-08-19)

### 31.1 Symptom

`constraints.sql`'s `cnn_pk` fixture promotes a pre-existing unique index into a
primary key:

```sql
ALTER TABLE cnn_pk ADD CONSTRAINT cnn_primarykey PRIMARY KEY USING INDEX cnn_uq;
```

PG renames `cnn_uq` to `cnn_primarykey` (with a NOTICE), marks it
`indisprimary`, and sets `attnotnull` on the key column. goopg accepted the
statement and did **nothing**: `\d+ cnn_pk` still showed `"cnn_uq" UNIQUE`, the
`Nullable` column stayed blank, and the "Not-null constraints:" footer was
missing. The sibling `ADD CONSTRAINT … UNIQUE USING INDEX` had the identical
defect.

### 31.2 Root cause — parser discard, not executor desync

The preceding census (§30.5 ranking) filed this under the "`Nullable`-blank
family" and assumed a catalog-heap sync gap like §30's path 2. That was wrong,
and the research pass that preceded this slice is the reason it did not cost a
wasted implementation round: the two other paths of that family had **already
been fixed** by M0134-0005o and M0134-0005v, and path 3's cause is upstream of
the executor entirely. `internal/parser/ddl.go` parsed `USING INDEX <name>`,
**threw the name away**, and downgraded the action to `AlterTableNoOp` — so
`execAlterTableAddPrimaryKey` never ran. No amount of executor-side tracing
would have found it.

### 31.3 The fix

- `internal/parser/ast.go` — `AlterTableAction.UsingIndexName string`.
- `internal/parser/ddl.go` — both the `PRIMARY KEY USING INDEX` and
  `UNIQUE USING INDEX` arms keep their real `Kind` and populate
  `UsingIndexName`; both also now parse the trailing
  `DEFERRABLE [INITIALLY DEFERRED]` per `gram.y:4249/4283`.
- `internal/executor/operators_ddl.go`:
  - `adoptExistingIndexAsConstraint` (new, shared by PK and UNIQUE) — ports
    `tablecmds.c:ATExecAddIndexConstraint`'s validation set (missing index
    `42704`; already-a-constraint `55000`; non-unique / expression / partial
    `42809`), renames on name mismatch with PG's exact NOTICE text, and sets
    `IsConstraint`/`Primary`/`Deferrable`/`InitiallyDeferred` + `resyncIndexHeapRow`.
  - `finishPrimaryKeyConstraint` (new) — the previously-inline PK tail,
    extracted so the USING-INDEX path reuses the *already-correct*
    not-null-synthesis block rather than cloning it. Its `IncludeColumns`
    write is now guarded so adopting an index cannot erase its own INCLUDE list.
  - `execAlterTableAddUnique` — the matching no-op stub replaced with the same
    helper at `primary=false` (Hard-won Rule #2: the twin shipped together).

### 31.4 Result

`scripts/pg-regress-runner.sh constraints`: **601 → 555 lines, 28 → 26 hunks.**
The sibling UNIQUE fix closed the `cnn_uq_idx` hunk on its own, beating the
24-line/1-hunk forecast — fixing the Rule-#2 twin was worth ~22 extra diff lines
here, not merely hygiene.

### 31.5 Worth carrying

**Re-verify a carried ranking's *cause*, not just its reachability.** The census
correctly measured that these hunks were open; its attributed root cause was
stale by two commits. A ~20-minute read-only research pass reclassified the bug
from "executor catalog-sync" to "parser discard" before any code was written.
On a diff being ground down slice-by-slice, carried causal attributions decay
faster than carried line counts.

### 31.6 Carried out of this slice (ledgered)

- Inline `CONSTRAINT … PRIMARY KEY (b)` **at CREATE TABLE time** sets
  `col.NotNull` and the footer but leaves `\d+`'s `Nullable` blank — the same
  heap-resync class fixed for `ALTER TABLE ADD CONSTRAINT` in M0134-0005o, never
  applied to CREATE TABLE's own inline-PK path. 2 of the remaining 26 hunks.
- `adoptExistingIndexAsConstraint` omits PG's "cannot use a deferrable index"
  rejection (`parse_utilcmd.c:2486-2492`): unreachable in goopg's model because
  the already-associated-with-a-constraint check fires first. Documented in-code.
