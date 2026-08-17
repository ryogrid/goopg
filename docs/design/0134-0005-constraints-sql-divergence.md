# M0134-0005 — `constraints.sql` divergence map + Buckets 1-3

Status: accepted — Buckets 1, 2 and 3 LANDED 2026-08-18; the case stays open (`[ ]`).
Running measurement: 1496 (baseline) → 1515 (B1, unmasking) → 1465 (B2) → **1431** (B3,
unmasking); hunks 30 → 30 → 31 → **33**.

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
| 4 | statement-level / deferred UNIQUE checking during self-referencing UPDATE (`UPDATE unique_tbl SET i=i+1`, ring rotations, `SET CONSTRAINTS … DEFERRED` re-check timing) | ~6-8 hunks, the largest line driver (~340 lines) | not one function: goopg maintains unique indexes row-by-row inside UPDATE | PG defers same-statement duplicate detection so a row-permutation UPDATE succeeds despite colliding intermediate states (`ExecInsertIndexTuples` + the deferred-trigger queue) | **MILESTONE**, not a slice — real constraint-timing/MVCC work |
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

## 7. Next slice for this case

**Bucket 4 is now the highest-value target**, promoted by this loop's measurement:
its deferred-UNIQUE defect is what caps Bucket 6's *and* (contrary to that row's
stated assumption) Bucket 2's remaining hunks. It is still milestone-sized —
statement-level/deferred UNIQUE checking is a real executor feature — so it needs a
research pass and its own decomposition, not a direct brief.

Cheaper alternatives, in order: the `get_nnconstraint_info` `(0 rows)` masking bug
(file-wide; unblocks the *observability* of several buckets at once and is the single
highest hunk-per-effort item if it is a shallow catalog-query gap); then Bucket 3's
leftover NOT NULL inheritance-propagation gap. Bucket 5 (GiST `circle_ops`) is a
milestone. **Bucket 7 still has no pinned root cause — do not brief from it.**
