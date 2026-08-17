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
