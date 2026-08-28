# 06 — goyacc Parser Playbook

Audience: coding agents modifying the goyacc-generated SQL parser or its
grammar (`grammar/*.y`) — including agents who are NOT doing parser work as
such, but are raising regress coverage or aligning behaviour with PostgreSQL
and find themselves in a `.y` file. You are assumed to know basic yacc/bison
concepts (rules, `$n`, shift/reduce) but nothing about this codebase. Follow
this document literally; every trap listed here was hit in production.

**Read §12 before you touch anything.** It inventories the places where this
grammar deliberately does NOT mirror `gram.y`, and why. Several of them look
like bugs and are not; changing them without reading the reasoning has broken
the parser before.

**The migration is complete.** There is ONE package, `internal/parser`, holding
both the goyacc parser and what remains of the hand-written one. The legacy
STATEMENT parsers were deleted in P7.2; what survives there is (a) the compat
token scanners for the ~1.7% of statement classes the grammar deliberately does
not own, (b) the lexer, the AST and the error machinery, and (c) shared helpers
the grammar's actions CALL. See §12.6.

Status (routed classes, conflict pin, deferred slices, the full migration
history) lives in `TODO.md` in this directory. Update it whenever you land a
wave.

---

## 1. File map — what to edit vs. what is generated

| file | role | edit? |
|---|---|---|
| `grammar/header.y` | `%union` fields, extra `%token`s (`TYPEDLIT`, `*_LA`), precedence | yes |
| `grammar/pg_grammar.y` | main grammar + ALL `%type` declarations | yes |
| `grammar/goopg_ext.y` | extension nonterminals (new DDL statements go here) | yes |
| `grammar/tokens_gen.y`, `grammar/kwlists_gen.y` | generated from `kwlist.h` by `gen-kwlist-go` | **no** |
| `tmp/goopg_grammar.y` | concatenation header→tokens→pg→kwlists→ext | **no** (regenerated) |
| `internal/parser/yacc_parser.go` | goyacc output | **no** (regenerated) |
| `internal/parser/adapter.go` | `lexerState`, `ParseOneSrc`, span capture | yes |
| `internal/parser/base_yylex.go` | keyword-substitution rules (`_LA` family) | yes |
| `internal/parser/dispatch.go` | routing tables (`routedStmts`, `routedCreatePairs`) | yes |
| `internal/parser/support.go` | carrier structs + helpers used by actions | yes |
| `internal/parser/yacc_ctors.go` | AST constructors (many structs have unexported `pos`) | yes |
| `internal/parser/lexer.go`, `token.go` | the hand-written LEXER — feeds the adapter | careful |
| `internal/parser/ast.go` | the AST both paths build | careful |
| `internal/parser/select.go`, `ddl.go`, `expr.go`, `function.go`, `copy.go`, `interval.go` | retained hand-written code: the compat token scanners, plus free-function helpers the grammar's actions CALL (§12.6) | careful |
| `internal/parser/testdata/parity_goldens.txt` | the oracle | regenerate, then REVIEW the diff (§9) |

`kwlist.h` is the source of truth for keyword names AND categories:
`grammar/tokens_gen.y` / `kwlists_gen.y` are generated from
`postgres/src/include/parser/kwlist.h` by `cmd/gen-kwlist-go`. A keyword's
category decides whether it can be a bare `ColId` — which is the answer to a
whole class of "PG accepts this and we do not" questions. Check the category
before adding a rule (§12.5 has two examples where the OLD parser was wrong
because goopg's lexer never classified the word at all).

Build entry point: **`make gen-parser`** only. Never run `go build` before it.
It regenerates the keyword files, concatenates the grammar, runs goyacc, and
enforces the conflict gate; `go build` alone will happily compile a stale
`yacc_parser.go`.

---

## 2. The golden loop

```
edit grammar/*.y
make gen-parser        # concatenates + goyacc + conflict gate
# on failure: decode the error (§3), fix, repeat
go build ./...         # ONLY after gen-parser succeeded
go test ./internal/parser/
```

To see the real goyacc error (make output is noisy):

```bash
make gen-parser 2>&1 | grep -vE '^go |^mkdir|^cat|^printf|gen-kwlist' | head -5
cat internal/parser/yacc_stderr.txt 2>/dev/null
```

`make gen-parser` deletes `y.output` at the end. To inspect states/conflicts:

```bash
cd internal/parser
go run golang.org/x/tools/cmd/goyacc -o yacc_parser.go -v y.output ../../tmp/goopg_grammar.y
grep 'shift/reduce' y.output | awk '{print $NF}' | sort | uniq -c
```

---

## 3. Error message decoder

| error text | meaning | fix |
|---|---|---|
| `nonterminal X not defined` | you used a token name that doesn't exist | `grep '"<word>"' internal/parser/keywords_gen.go` — many keywords carry `_P` (`IN_P`, `TYPE_P`, `DATA_P`, `IDENTITY_P`, `ADD_P`) but not all (`REPLICA`, `MATERIALIZED`, `CREATE` have no suffix). Always grep first. |
| `must specify type for 'X'` | an action's `$n` resolved to TOKEN `X`, which has no `%union` field | `X` names the exact symbol your `$n` landed on → your index is wrong. Recount (§4). |
| `Illegal use of $N` | `$N` exceeds that alternative's symbol count | recount INCLUDING keywords and mid-rule `{...}` actions (§4). |
| `cannot use yyDollar[3].qn (variable of struct type qname) as parser.ObjectName value` | `%type` mismatch: `qualified_name` is `<qn>`, not `<oname>` | use `objectNameFromQn($n)`; check the nonterminal's `%type` before writing the action. |
| `undefined: yyLexer` / `undefined: yyParse` at `go build` | goyacc failed and left a 59-line stub `yacc_parser.go` | **do not debug Go code** — the grammar is broken. Rerun `make gen-parser` and read `yacc_stderr.txt`. |

---

## 4. Position arithmetic (`$n`) — the #1 source of errors

`$n` counts **every symbol in the alternative, including keywords and mid-rule
actions**.

```
ALTER opt_COLUMN ColId SET DEFAULT a_expr
  1        2       3    4      5      6     → a_expr is $6, NOT $5

CREATE opt_or_replace VIEW qualified_name opt_name_list_p AS { markSpanStart() } SelectStmt
  1          2          3         4                5         6        7 (synthetic)      8     → SelectStmt is $8
```

Rule: after ANY edit to an alternative (adding/removing a symbol or a mid-rule
action), re-derive every `$n` in that action from scratch. Do not eyeball.

---

## 5. Recipe: adding a new statement class

Canonical example: ALTER TABLE (P4.2). Do the steps in order.

1. **Probe first — never guess AST shapes, enum values or positions.**

   ```go
   // internal/parser/zz_probe_test.go (temporary — DELETE when done)
   func TestProbe(t *testing.T) {
       toks, _ := Lex("ALTER TABLE t ADD COLUMN c int")
       sts, err := ParseOneSrc("ALTER TABLE t ADD COLUMN c int", toks)
       t.Logf("%v %s", err, dumpStmts(sts))
       t.Logf("%+v", sts[0]) // %+v is how you see the unexported pos fields
   }
   ```

   Record: exact `Kind` enum values, which fields are populated, default
   values, position semantics.

   Before P7.2 this step said "probe the LEGACY parser" — it was the oracle.
   It is gone. The oracle now is **the golden corpus** (§9) plus `./postgres/`
   itself: `gram.y` for what a form should parse to, and
   `src/test/regress/expected/*.out` for what PG actually prints, INCLUDING
   whether an error carries a `LINE n:` caret. Cite the file and line in your
   comment — every non-obvious decision in this grammar does.

   A `zz_probe_test.go` left in the tree pollutes two things: the golden
   corpus (`assertParity` records what it sees) and `harvestSQLLiterals`,
   which scans this package's own test files. Delete it before you finish.

2. **Add a constructor** in `internal/parser/yacc_ctors.go` if the struct has
   unexported fields (`pos`) — actions cannot build such structs directly
   from another package.

3. **Write the grammar** in `grammar/goopg_ext.y`. Prefer flat alternatives;
   reuse existing nonterminals (`qualified_name`, `col_type_name`,
   `colid_list`, `drop_name_list`, `opt_drop_behavior`, …).

4. **Declare `%type`** for every new nonterminal in `pg_grammar.y` next to
   siblings of the same type. New union field needed? Add it to
   `grammar/header.y` `%union` first.

5. **Wire the statement into `stmt:`** in `pg_grammar.y` (add a `| my_stmt`
   alternative).

6. **`make gen-parser`.** The conflict gate pins an exact count (read it from
   the Makefile — `grep -oE '\-ne [0-9]+' Makefile`; it was 59 when this was
   written) plus a token allowlist. If the count grew, find out why BEFORE
   bumping the pin (§6).

7. **Pin the forms.** Move your probe cases into the permanent suite as
   `assertParity(t, q)` (accepted) or `assertBothReject(t, q)` (refused), in a
   file next to its siblings. Then regenerate the goldens and **READ THE
   DIFF** — that diff is the review artifact for your change (§9). Delete the
   `zz_` probe file.

8. **Flip routing** in `internal/parser/dispatch.go` (no wiring needed any
   more — `Parse` calls `routeBatch` directly; the function-pointer hook that
   postmaster used to install is deleted):
   - single-keyword classes → `routedStmts`
   - two-keyword classes (`create`/`drop`/`alter`) → `routedCreatePairs`
   - note: `secondKeywordRouted` skips `or`/`replace` but deliberately does
     NOT skip `temp`/`temporary`/`unlogged` — those statements fall back to
     the legacy parser (shared modifier prefixes with CREATE TABLE create
     irreducible S/R conflicts).

9. **Live-verify on the TPC-H goopg cluster** (port 65433):

   ```bash
   export PATH="$PWD/postgres/local_install/bin:$PATH"
   go build -o tmp/goopg-bench-bin ./cmd/goopg
   bench/tpch/stop_goopg.sh; sleep 1; bench/tpch/setup_goopg.sh; sleep 1
   psql -h 127.0.0.1 -p 65433 -U postgres -d tpch -c "..." -c "..."
   ```

   `setup_goopg.sh` rebuilds the binary itself, but **it may skip the start
   if it thinks a server is running** — always stop first, and never trust
   `pgrep -f goopg` (it self-matches the invoking shell).

10. **Update `TODO.md`** (wave status, known diffs, deferred slices) and
    commit with explicit pathspecs (§10).

---

## 6. Conflict discipline

The Makefile gate (`gen-parser` target) enforces: exact pinned S/R count AND
every conflict token must be on the allowlist. Both the `-ne N` comparison and
the message string must be updated together (they drifted once and the gate
silently compared against the wrong number).

**Known S/R classes** (each maps to a documented upstream or goyacc artifact):

| token | count grows by | cause |
|---|---|---|
| `'('` ×2 | — | func_call/extract vs parenthesized expr |
| `'['` ×2 | — | subscript ambiguity |
| `ON` | — | join vs ... |
| `IF_P` | +1 per grammar rule using `opt_if_exists_drop` / `opt_if_not_exists` | the two "if exists" nonterminals share states |
| `NOT` | — | DEFAULT vs FK action |
| `SESSION`/`LOCAL` | — | optional scope keywords |

So: adding a statement that uses `opt_if_not_exists` legitimately bumps the
pin by exactly 1 (`IF_P`). Any other new conflict token = your bug.

**A rule ends at the next `IDENT ':'`, not at a `;`** — this grammar uses no
rule terminators. Inserting a new nonterminal in the MIDDLE of an existing
rule therefore silently reassigns that rule's remaining alternatives to the
new one, with no warning from goyacc. This happened in `2a55eff04` (the FK
helper rules landed inside `col_constraint`, orphaning its `PRIMARY KEY`,
`UNIQUE` and `DEFAULT` alternatives onto `fk_kw`) and disabled column-level
PRIMARY KEY/UNIQUE/DEFAULT in the routed CREATE TABLE path — plus made
`ON DELETE PRIMARY KEY` panic on a type assertion. **Append new alternatives
to a rule directly under its existing ones, and after any insertion re-read
the rule you inserted into, end to end.**

**R/R (reduce/reduce) conflicts are almost always your bug.** Historical
causes in this repo:

- stale duplicate grammar block at file end (once caused "3927 R/R" — the
  number was an artifact, the grammar was the problem);
- `opt_ct_tail` containing `AS SelectStmt` while a separate
  `create_table_stmt_as` rule existed → fixed by an `opt_ct_tail_noas` variant
  excluding `AS`;
- empty alternative vs. mid-rule action nonterminal on the same lookahead;
- `VALUES` as statement vs. `VALUES` as a `col_name_keyword` identifier →
  fixed with the `_LA` lexer substitution (`base_yylex.go`): substitute
  `VALUES→VALUES_LA` when `'('` follows. Prefer `_LA` substitution over
  restructuring when a keyword must stay usable as an identifier.

**Mid-rule action trap:** a *named* empty nonterminal used only as a marker
(`foo: /* empty */ { code }`) may silently not emit its action in goyacc.
Use **inline** mid-rule actions instead:

```
| AS { yylex.(*lexerState).markSpanStart() } SelectStmt
```

(The `CHECK` constraint rule uses the same pattern — copy it.)

---

## 7. Raw source span capture (CHECK text, SET values, view `RawDef`)

Plumbing: `RouteBatch(src, toks)` carries the original SQL → `ParseOneSrc`
stores it in `lexerState.src`. Legacy equivalent: `captureSrcSpan`.

- **Start**: inline mid-rule `markSpanStart()` → records `peek().pos` (the
  NEXT token's position).
- **End, normal case**: `spanTextUpTo(l.fragEnd)`. `fragEnd` is computed by
  `fragEndPos(src, toks)` at `ParseOneSrc` entry = end of the fragment's last
  real token; a trailing `;` contributes its START (so the span excludes it).
- **End, statement has a trailing optional clause** (e.g. `WITH [NO] DATA`):
  use the `with_data_kw` pattern — shift the clause's first keyword, then in
  the action set `l.endMark = l.prevPos + len(l.prevText)` (end of the token
  *before* the keyword = end of the body). `spanEnd()` returns `endMark` when
  set, else `fragEnd`. **`endMark` must be initialized to −1** in both
  `ParseOneSrc` and `ParseOne` — its zero value 0 is a valid-looking byte
  offset and silently breaks spans.
- **Never use `peek()` or `lastPos/lastText` at statement-final reduce time**:
  the EOF lookahead is already consumed (`peek()` reads past the token buffer
  → zero value; `lastText` is explicitly cleared on EOF). That is why the
  fragment-end carrier exists.

---

## 8. Dispatch / routing

```
Parse(input)
  └─ routeBatch(src, toks)         // direct call; no hook since P7.1
       └─ SplitStatements → fragmentRouted per fragment
            ├─ first token "with"        → withFollowerRouted
            ├─ "create"/"drop"/"alter"   → secondKeywordRouted (pair map)
            └─ else                      → routedStmts
```

- If ANY fragment in a batch is not routed, **the whole batch** falls back to
  the legacy path. Errors from the yacc parser (`handled=true, err!=nil`)
  surface directly — no silent fallback AFTER routing.
- **That fallback is the most dangerous thing in this file**, because it is
  silent and the thing it falls back to still works. Three defects hid in it
  for the whole migration and no test could see them:
  an empty fragment vetoed the batch (`BEGIN; COMMIT;;` — one stray semicolon
  selected the old parser for everything); `firstMeaningful` counted a bare
  `;` as a statement; and a CTE whose NAME is a keyword
  (`WITH index AS (…)`, which pg_amcheck emits) was read as the follower
  query, so `routedStmts["index"]` — false — sent the batch to legacy.
  If you change `fragmentRouted`, `firstMeaningful` or `withFollowerRouted`,
  verify with a probe that the statement you care about reports
  `fragmentRouted(frag) == true`; a passing test proves nothing here.
- Unit tests need no routing setup: `Parse` routes unconditionally.

---

## 9. Test gates

- Fast iteration (~0.3 s): `go test ./internal/parser/`.

**The golden corpus is the oracle.** `testdata/parity_goldens.txt` holds ~1,640
statements, each with the AST dump the parser produced — or `!<message>` when
it refused. `assertParity` / `assertBothReject` compare against it. Every entry
was captured while the LEGACY parser still existed and agreed (the migration's
ratchets stood at zero rejects and zero AST divergences), so a golden IS the
legacy answer, recorded. That is what let P7.2 delete the legacy statement
parsers without losing the oracle.

```bash
GOOPG_UPDATE_GOLDENS=1 go test ./internal/parser/   # regenerate
git diff internal/parser/testdata/parity_goldens.txt # ← REVIEW THIS
```

- **A golden diff is a review item, never a formality.** It says a pinned AST
  changed. If you cannot explain each line, you have a bug.
- Regeneration writes from `TestMain`, which is the only hook that runs after
  every test. It used to be a test named `TestZZZParityGoldensRegenerate` on
  the theory that "ZZZ" sorted last; `go test` runs tests in SOURCE ORDER
  (file by file, alphabetically), so it ran before every file after "g" and
  recorded nothing for them — the corpus was missing half the statements it
  existed to pin. Do not move it back into a test.
- `assertParity` FAILS on a statement with no golden rather than passing
  vacuously. If you add a pin, you must regenerate.
- Anything that parses SQL in this package must call **`ParseOneSrc`, never
  `ParseOne`**. `ParseOne` leaves `lexerState.src` empty, so every raw-source
  span (`RawDef`, `CheckExpr`, SET values) comes back `""` and a whole class of
  field silently compares equal.
- `tpch_coverage_test.go` pins 22/22 TPC-H queries parsing; it is an EXTERNAL
  test package (`parser_test`) because `internal/testutil/tpch` imports
  `internal/parser`.
- Planner/executor-touching changes additionally: `scripts/tpch-spotcheck.sh`
  (fresh capped server; canonical Q12/Q13 row counts).
- The FULL `go test ./internal/testport/` wedges in `TestPort_IsolationSuite`
  — a harness problem, not a code one. Run the two halves separately:
  `-run TestPort_IsolationSuite` (~30 s) and `-run TestPort_RegressSuite`
  (~5 min). Judge a long run by EVIDENCE OF PROGRESS (diff-file count,
  `cluster.log` mtime), never by elapsed time. Reap leftover scopes with
  `systemctl --user stop goopg-verify-<name>.scope` — `kill` is the wrong
  tool and the orphans are `ppid=1`.
- **Never pass `-count=1` to a gate** — it defeats the test-result cache.
- Live cluster hygiene: hold server age constant across A/B comparisons;
  `timeout N psql` kills only the client (the server keeps executing).

---

## 10. Git discipline

- Commit style: `area(scope): summary — detail`
  (e.g. `parser-rewrite(P4.2): ALTER TABLE multi-action comma lists`).
- Stage by explicit pathspec (`git add -A -- grammar/ internal/parser/ ...`)
  — a concurrent Ralph loop's WIP may be present. Never bare `git add -A`.
- Never `git commit --no-verify` — the hook runs the pgbench smoke.
- Push the branch you were given, and only that one. A concurrent Ralph loop
  works in the same tree on its own branch; pushing parser commits onto it
  mixes two unrelated tracks (this happened once, onto `regress-renumbering`).

---

## 11. Pitfall checklist (symptom → cause)

- `go build` fails with `undefined: yyLexer` → goyacc failed; fix grammar.
- Conflict count jumped by 1 on `IF_P` → you added an `opt_if_exists*` user;
  legitimate — bump the pin AND the message in the Makefile.
- Conflict count jumped on any other token → your bug; inspect `y.output`
  (state number → the two rules).
- `must specify type for ')'` → `$n` points at `')'`; recount symbols.
- Action assigns a `qn` where `ObjectName` is wanted → `objectNameFromQn`.
- Captured span is empty despite `markSpanStart` → `endMark` not initialized
  to −1, or you used `peek()` at stmt-final reduce.
- Captured span includes a trailing clause (`WITH DATA`) → missing
  `with_data_kw`-style end marker.
- A statement you flipped still behaves like the old parser → it is not
  actually routed. Probe `fragmentRouted(frag)` directly; the fallback is
  silent and whole-batch (§8).
- `assertParity` says "no golden for …" → you added a pin without
  regenerating (§9).
- A form the golden pins as `!syntax error …` suddenly produces an AST → you
  WIDENED the grammar. Decide deliberately: PG may accept it (then it becomes
  an `assertParity` with a `gram.y` citation) or it may not (then it is a
  divergence — fix it, or record it in §12.5 with the citation).
- Server ignores new grammar after restart → stale server: `setup_goopg.sh`
  skipped the start. Stop first; verify the port is free (`ss -ltn`).
- "no partition of relation found" on INSERT into a partitioned parent →
  pre-existing EXECUTOR gap, not a parser bug (parser output verified
  identical to legacy).

---

## 12. goopg-specific accommodations — read this before editing a `.y` file

This grammar is a port of `postgres/src/backend/parser/gram.y`, but it is not a
copy. The differences below are DELIBERATE. Several of them look like bugs.
If you are here to raise regress coverage or to align a behaviour with
PostgreSQL, this section is the map of what you are allowed to change freely,
what you must change carefully, and what you must not change without reading
the reasoning first.

The general rule: **this grammar's oracle is goopg's own AST and error surface,
not gram.y's parse tree.** goopg has no `Node`/`ParseState`; it has its own AST
types with unexported `pos` fields, and its executor reads specific fields. A
change that makes the grammar look more like upstream but changes what the AST
carries is a regression, not an improvement.

### 12.1 Synthetic terminals the real scanner does not have

The lexer is goopg's hand-written one (`internal/parser/lexer.go`), not
`scan.l`. `internal/parser/adapter.go` maps its tokens onto goyacc terminals,
and in four places it FOLDS several source tokens into one synthetic terminal
that has no counterpart in gram.y. If you `grep` gram.y for these you will find
nothing — that is expected.

| terminal | folds | why |
|---|---|---|
| `TYPEDLIT` | `IDENT SCONST` for date/time/etc (`date '2020-01-01'`) | `str` carries `"type\x1fvalue"`; split with `typedLitParts` |
| `CHECKBODY` | `CHECK ( … )` whole | `parseCheckExpr` never parsed the body — it stored a TOKEN JOIN, so `CHECK ((y).a > 0)` is legal where `a_expr` would refuse it. The terminal carries the two PAREN offsets: `pos` = `(`, `ival` = `)`. **The CHECK keyword's own offset is not carried** — that is why `ADD CHECK`'s action position is a known divergence (§12.5). |
| `*_LA` family | `NOT`, `WITH`, `WITHOUT`, `NULLS_P`, `VALUES`, `FORMAT` when a specific follower matches | mirrors upstream's `base_yylex` (`parser.c:111-251`). Prefer adding an `_LA` substitution over restructuring a rule when a keyword must stay usable as an identifier. |
| char typmod | `char(20) 'x'` | `peekCharTypmodLit` — a typmod'd typed string literal |

**Consequence for regress work:** if a statement parses in PG but not here and
the difference involves one of these shapes, the fix may belong in
`adapter.go` / `base_yylex.go`, not in the `.y` file. Check there first.

### 12.2 `$<p>N` — the position primitive, and when `lastConsumedPos()` lies

goyacc has no `@n` locations. goopg adds a `p int` field to `yySymType`
(`grammar/header.y`) that the adapter fills with each terminal's absolute byte
offset, so **`$<p>N` is this grammar's `@N`**. Use it.

`lexerState.lastConsumedPos()` exists too and is a trap:

> `lastConsumedPos()` returns `prevPos`, which names the current token ONLY IF
> a lookahead has actually been read. Where the rule is a DEFAULT REDUCTION it
> names the token BEFORE; where the rule reduces after a trailing operand it
> names one AFTER.

Both directions shipped. `qualified_name` used it and zeroed the position of
**every FuncCall in the language**; `select_pos` used it and gave `SELECT 1` a
position PAST THE END of the statement. The grammar went from 94 uses to a
handful where a lookahead is guaranteed. **Default to `$<p>N`.** If you think
you need `lastConsumedPos()`, you probably need `$<p>N` of a symbol you have
not counted yet (§4).

### 12.3 Position conventions — `X.Pos()` is not "where X starts"

Positions are not decoration: `AlterTableAction.pos`, `ColumnDef.pos`,
`ObjectName.pos` and every expression node's `pos` feed `ExecError.Pos`, which
becomes the wire ErrorResponse `Position` and the caret psql draws. There are
FOUR conventions, and they interact:

1. **Operator-anchored nodes.** `BinaryOp`, `InExpr`, `IsDistinctFromExpr`,
   `IsNullExpr`, `IsBoolExpr`, `CastExpr`, `CollateExpr`, `LikeEscapePattern`,
   `BETWEEN`, `SIMILAR TO`, `AT TIME ZONE` all anchor at their OPERATOR token
   (`>`, `IN`, `::`, `COLLATE`, `ESCAPE`, …) — not at the left operand.
2. **Construct-start nodes.** `ResTarget` and `SortBy` anchor at the FIRST
   TOKEN of the target/sort item. These two are why (1) is dangerous to
   change: they were correct while every expression node pointed at its own
   first token, and broke the moment `BinaryOp`/`CollateExpr` moved to the
   operator. **After changing any node's anchor, re-check everything that was
   reading `X.Pos()` as a proxy for "where X starts".**
3. **Nested vs top-level queries.** A query parsed as an EMBEDDED one — a CTE
   body, a set-operation arm, a scalar subquery, a derived table, CTAS's
   source, INSERT's source, EXPLAIN's inner statement — carries its leading
   keyword's offset. The plain TOP-LEVEL statement carries ZERO. `WITH … SELECT`
   is not plain (it goes through a different legacy entry, which stamped it).
   The grammar has one rule for all of them, so `simple_select` stamps
   unconditionally and `stmt_list` un-stamps via `topLevelSelect` when
   `With == nil`. The same holds for INSERT/UPDATE/DELETE.
   `scopePartitionByPos` is the same shape for `PARTITION BY`.
4. **Nodes that legitimately carry ZERO.** `SelectStmt` and the
   transaction-control statements at top level; the per-column `ALTER COLUMN
   SET/DROP …` actions; `PartitionByClause` at top level. Matching means
   PASSING 0, not inventing an offset. A wrong caret is worse than none.

`TestPositionParity` used to guard all of this against the legacy parser — it
walked both ASTs by reflection and compared every `pos` — and retired with
P7.2 at a residual of 4. `dumpStmts` does NOT print positions and the goldens
therefore do not record them, so **position regressions are no longer caught
automatically.** `%+v` on a statement shows them; if you are changing anchors,
write a throwaway reflection walk (git history: `pos_parity_test.go`,
`collectPositions`) rather than eyeballing. If you touch an
anchor, verify by hand against `./postgres/src/test/regress/expected/*.out` —
those files show whether PG prints a `LINE n:` caret at all, which is the
question that settles most of these (upstream's `AlterTableCmd`, for one, has
NO location field, so PG emits no caret for `DETACH PARTITION` errors and zero
is the faithful answer).

### 12.4 Deliberate laxity: the compat scanners still on the legacy path

~1.7% of the regress corpus does not reach the grammar. Two different reasons,
and neither is a gap to be closed:

- **Intercepted ABOVE the parser.** `internal/postmaster/dispatch.go`'s
  `compatNoopCommandTag` matches role DDL, GRANT/REVOKE and database/schema
  DDL by STRING PREFIX before `parser.Parse` is called. A grammar rule for
  these would be dead code.
- **Parse-and-ignore token walks.** `CREATE`/`ALTER OPERATOR`, TEXT SEARCH,
  `CREATE CAST`, `CREATE RULE`, `CREATE AGGREGATE`, `CREATE STATISTICS`,
  `ALTER DEFAULT PRIVILEGES`, FOREIGN/SERVER/CONVERSION. Their handlers end in
  `parseSkipToSemicolon` and therefore accept arbitrary token soup. **A real
  grammar for them would be STRICTER than what ships.** goopg does not execute
  these — they exist to round-trip DDL for pg_dump — so porting one can only
  start rejecting inputs the server accepts today.

If a regress case fails because one of these is not modelled, the fix is
almost certainly in the executor or the compat scanner, **not** a new grammar
rule. Before writing one, check whether the legacy handler is strict (errors on
bad input) or lax (skips to `;`): `EVENT TRIGGER` and `ACCESS METHOD` were the
only two strict ones left and both are ported.

### 12.5 Known, deliberate divergences from gram.y

| what | direction | why |
|---|---|---|
| `-5` folds into `IntegerConst{-5}` | matches gram.y's `doNegate`; the OLD hand parser built `UnaryOp` | anything that was a fixed point of the old shape had to move — `nodes.rebuildConst` did |
| `CREATE TABLE … WITH (…) PARTITION BY …` accepted | LAXER than PG, which fixes the tail order (`gram.y:3633`) | `ct_tail_list` is order-free because a repeated `WITH` must work and ddl.go's CREATE INDEX tail is order-free too |
| `CREATE TABLE t (user text)`, `(verbose text)` REJECTED | STRICTER than the old parser, matches PG | `kwlist.h:480` makes `user` RESERVED, `:491` makes `verbose` TYPE_FUNC_NAME; `ColId` reaches neither |
| `ALTER TABLE … ADD CHECK` action position | differs from the old parser | `CHECKBODY` carries the paren offsets, not the keyword's (§12.1) |
| `SELECT f(variadic array[…]::int[])` cast position | differs | the VARIADIC argument path anchors the cast at the array's first element |

When you find a NEW divergence, decide it against `./postgres/`, not against
what the code used to do — and record it here with the citation.

### 12.6 The grammar CALLS into the retained hand-written files

`select.go`, `ddl.go`, `expr.go`, `function.go`, `copy.go` and `interval.go`
are not dead. The grammar's actions call free functions in them —
`normalizeFloatTypeName`, `decodeBitStringLit`, `foldSubstringSimilar`,
`resolveColumnNotNull`, `nearTextOf`, the `similarto` and interval helpers.
**Editing one changes behaviour on both paths**, which is the point: every
time this migration found a second copy of one of these, the copy was the LAX
one (three inline float folds without `opt_float`'s range checks; a
`bitStringConst` with no digit validation; a `SUBSTRING … SIMILAR` fold that
dropped both SQLSTATEs). If you need behaviour that exists in one of those
files, CALL it — do not reimplement it in an action.

`internal/parser/support.go` holds the carrier structs and helpers that exist
only for the grammar. `internal/parser/yacc_ctors.go` holds constructors for
AST types with unexported fields. New helper → `support.go`; new AST
constructor → `yacc_ctors.go`.

### 12.7 Error surface

`SyntaxError` has ONE shape on both paths: `Message` holds the offending
token's RAW SOURCE spelling (a string literal keeps its quotes, a quoted
identifier keeps its, a plain identifier is bare — `nearTextOf`), and
`Error()` supplies the `syntax error at or near …` wrapper. Do not build the
whole sentence yourself with `Raw: true`; that produced an identical
user-visible string but a different FIELD, which every caller reading
`err.Message` could see.

For a SEMANTIC error raised from inside an action — upstream's
`ereport(ERROR, …)` from a production, e.g. `opt_float`'s 22023 — use
`raiseErr(l, &SyntaxError{…})`, which carries `Code` and `Hint`.
`lexerState.Error()` rewrites everything into "syntax error at or near", which
is exactly wrong for these, and `l.raise()` has no `Code` parameter.

### 12.8 Habits this migration paid for

- **Make a guard fail on purpose once before trusting it.** Three tests in
  this package looked like guards and were structurally unable to fail:
  `TestLegacyCorpusParity` `continue`d whenever either parser rejected (which
  let ~30 gaps accumulate); `assertBothReject` checked a condition that could
  never be true; the golden regenerator ran before half the corpus.
- **Measure, do not reason, about reachability.** P7.2's deletion was driven
  by probes that recorded a stack trace from each legacy arm, run against the
  whole unit corpus — not by reading the routing tables. That loop found four
  routing defects; reading would have found none, because a fallback to a
  parser that still works looks like success.
- **Change one thing and re-measure.** Stamping `PartitionByClause.pos`
  unconditionally looked obviously right and made position parity WORSE
  (83 → 90). Two edits landed together and the total moved the wrong way.
