# root-0039 — "missing FROM-clause entry" swallowed the alias diagnosis

Status: accepted
Date: 2026-08-06
Milestone: M-NIGHTLY (AI-20260806-011323-016), selected under the S7-gate carve-out
Files: `internal/analyzer/analyzer.go`, `internal/planner/planner.go`,
`internal/analyzer/missing_rte_test.go`, `internal/planner/missing_rte_test.go`

## 1. The symptom, and why it was selected

Nightly `20260806-011323` reported three baseline-pass regress divergences.
`select` was a backend crash (fixed, 07 §4a) and `portals_p2` never reproduced;
`delete` was left open as "a real PG-compat gap … error wording", with a
deferral-ledger row naming `errorMissingRTE()` as the resume point.

It is the one of the three that **reproduces deterministically**, and it is a
blocker for M0127's S7 bar — P6.1–P6.4 wait on "S5-ON survives a clean nightly
cycle", and a case that diverges every night can never let the cycle come back
clean. Same carve-out reading the harness fix (ci/design/04 §C.1) was taken
under: the gate is not merely noisy, it is unmeetable while this stands.

Upstream `delete.sql` is one statement:

```sql
DELETE FROM delete_test dt WHERE delete_test.a > 25;
```

PG:

```
ERROR:  invalid reference to FROM-clause entry for table "delete_test"
HINT:  Perhaps you meant to reference the table alias "dt".
```

goopg:

```
ERROR:  missing FROM-clause entry for table "delete_test"
```

## 2. The real shape of the defect — it was never about DELETE

The ledger row scoped this to DELETE. It is not: goopg emitted the bald message
for **every** shape upstream distinguishes. Probed on a throwaway cluster before
the fix, all five gave the identical wrong text:

| shape | upstream corpus |
|---|---|
| `DELETE FROM t dt WHERE t.a > 25` | `delete.out` |
| `UPDATE t AS x SET b = t.b + 10` | `update.out` |
| `INSERT INTO t AS bar … RETURNING t.*` | `returning.out` |
| `INSERT INTO t AS ict … ON CONFLICT … SET f = t.f` | `insert_conflict.out` |
| `SELECT t.a FROM t dt` / `SELECT t.* FROM t dt` | (the general form) |

The distinction goopg was missing is a *diagnosis*, not a wording preference.
`errorMissingRTE()` (`postgres/src/backend/parser/parse_relation.c`) does not
conclude "the relation is absent" when a qualified reference fails to resolve.
It looks the refname up a **second time ignoring aliases**
(`searchRangeTableForRel`), and if that finds a FROM entry the user renamed, the
mistake is "you wrote the table's own name where only its alias is visible" —
a different error with a different remedy, which is why upstream gives it its
own message plus a HINT naming the alias. Only a refname matching nothing at
all gets the bald message.

goopg performed the first lookup and stopped. There was no second lookup
anywhere in either name resolver, so "renamed" and "absent" collapsed into one
outcome.

### 2.1 Why the existing `blockOriginalName` machinery did not cover it

goopg already had a narrow version of this: `scopeRel.blockOriginalName` /
`rangeBinding.blockOriginalName` (M0097-0003) produce exactly the right message
and hint — but only for the two call sites that *set* the flag (ON CONFLICT's
primary binding, and the planner's DELETE target). The analyzer's `analyzeDelete`
never set it, and the analyzer runs first, so the planner's correct error for
`delete.sql` was unreachable. Fixing that one call site would have turned
`delete` green while leaving `update` / `returning` / `insert_conflict` and every
plain SELECT wrong — the "green test with undocumented missing semantics" shape.

The flag is kept: it fires at the level where the aliased rel lives and so stops
the chain walk before an outer scope can match. Both mechanisms now agree on
message, hint, and code.

## 3. The change

One helper per resolver, mirroring `errorMissingRTE()`:

- `analyzer.errorMissingRTE(pos, schema, table, *scope)`
- `planner.errorMissingRTEPlan(pos, schema, table, *resolveContext)`

Each walks its own lexical chain (`scope.parent` / `resolveContext.parent`)
looking for a rel whose *underlying catalog name* matches the refname but which
carries a different alias; on a hit it returns the invalid-reference message
plus the alias HINT, otherwise the bald missing-entry message.

Two exclusions, both faithful:

- `qualifiedOnly` rels (ON CONFLICT's `excluded`) share the target's
  `*catalog.Table`; their "alias" is a keyword the user did not choose, so
  hinting it would actively mislead.
- an entry aliased to its own name is skipped — upstream's
  `strcmp(eref->aliasname, relname) != 0` guard. Without it,
  `SELECT t.a FROM t t` would error with a hint naming `t`.

Call sites rewired: the analyzer's qualified-column fallthrough
(`resolveColumnRefType`) and `analyzeStar`; the planner's qualified-star
expansion. **Sibling-path rule**: the analyzer is the first error source for the
statements it covers, but RETURNING scopes are built after analysis, so the
planner is the *only* source for `RETURNING t.*` — the wire probe confirmed that
case is served by `errorMissingRTEPlan`, which is why both twins moved.

### 3.1 Error code corrected: 42712 → 42P01

The two pre-existing `blockOriginalName` errors raised **42712**
(`duplicate_alias`). Upstream raises this from `errorMissingRTE()` with
`ERRCODE_UNDEFINED_TABLE` on all three branches — 42P01. Invisible in regress
output (psql prints the message, not the code), which is how it survived; it is
still wrong on the wire for any client that keys off SQLSTATE.

## 4. What is deliberately not ported

`errorMissingRTE()` has a **middle** branch: a range-table entry that exists but
is not visible from this part of the query (`SELECT … FROM a, b LEFT JOIN c ON
(a.x = c.y)`), which emits an `errdetail` and, when
`rte_visible_if_lateral()` holds, a "you must mark this subquery with LATERAL"
hint. goopg's scope chain carries no present-but-invisible entries to key that
off — a rel is either in a reachable scope or it is not — so that branch would
need a scope model change, not a message change. Ledger row filed.

## 5. Verification

- `TestPort_RegressSuite/delete` **SKIP(deferred) → PASS**. A/B on two builds
  differing only in these two files: at HEAD it is SKIP, with the fix it is PASS.
- `insert_conflict`, `returning`, `subselect`, `update` were SKIP on **both**
  sides of that A/B — they are deferred for unrelated reasons and this change
  neither fixes nor regresses them. `select` and `portals_p2` PASS on both.
- Guards `TestAnalyzeErrorMissingRTEAliasHint` (5 analyzer-owned shapes + 2
  negative controls: absent relation keeps the bald message and no hint;
  self-aliased relation resolves) and `TestPlanErrorMissingRTEAliasHint`
  (RETURNING star + unaliased control). Both verified **non-vacuous** — with the
  two source files reverted and the tests kept, both fail with
  `missing FROM-clause entry`.
- Wire probe against a throwaway cluster reproduces all four upstream corpus
  shapes byte-for-byte, including the HINT line.
