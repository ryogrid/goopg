# 06 — goyacc Parser Playbook

Audience: coding agents modifying the new goyacc-generated SQL parser
(`internal/sqlparser`) or its grammar (`grammar/*.y`). You are assumed to know
basic yacc/bison concepts (rules, `$n`, shift/reduce) but nothing about this
codebase. Follow this document literally; every trap listed here was hit in
production.

Status of the migration (which statement classes are routed, current conflict
pin, deferred slices) lives in `TODO.md` in this directory. Update it whenever
you land a wave.

---

## 1. File map — what to edit vs. what is generated

| file | role | edit? |
|---|---|---|
| `grammar/header.y` | `%union` fields, extra `%token`s (`TYPEDLIT`, `*_LA`), precedence | yes |
| `grammar/pg_grammar.y` | main grammar + ALL `%type` declarations | yes |
| `grammar/goopg_ext.y` | extension nonterminals (new DDL statements go here) | yes |
| `grammar/tokens_gen.y`, `grammar/kwlists_gen.y` | generated from `kwlist.h` by `gen-kwlist-go` | **no** |
| `tmp/goopg_grammar.y` | concatenation header→tokens→pg→kwlists→ext | **no** (regenerated) |
| `internal/sqlparser/yacc_parser.go` | goyacc output | **no** (regenerated) |
| `internal/sqlparser/adapter.go` | `lexerState`, `ParseOneSrc`, span capture | yes |
| `internal/sqlparser/base_yylex.go` | keyword-substitution rules (`_LA` family) | yes |
| `internal/sqlparser/dispatch.go` | routing tables (`routedStmts`, `routedCreatePairs`) | yes |
| `internal/sqlparser/support.go` | carrier structs + helpers used by actions | yes |
| `internal/parser/yacc_ctors.go` | AST constructors (many structs have unexported `pos`) | yes |

Build entry point: **`make gen-parser`** only. Never run `go build` before it.

---

## 2. The golden loop

```
edit grammar/*.y
make gen-parser        # concatenates + goyacc + conflict gate
# on failure: decode the error (§3), fix, repeat
go build ./...         # ONLY after gen-parser succeeded
go test ./internal/sqlparser/
```

To see the real goyacc error (make output is noisy):

```bash
make gen-parser 2>&1 | grep -vE '^go |^mkdir|^cat|^printf|gen-kwlist' | head -5
cat internal/sqlparser/yacc_stderr.txt 2>/dev/null
```

`make gen-parser` deletes `y.output` at the end. To inspect states/conflicts:

```bash
cd internal/sqlparser
go run golang.org/x/tools/cmd/goyacc -o yacc_parser.go -v y.output ../../tmp/goopg_grammar.y
grep 'shift/reduce' y.output | awk '{print $NF}' | sort | uniq -c
```

---

## 3. Error message decoder

| error text | meaning | fix |
|---|---|---|
| `nonterminal X not defined` | you used a token name that doesn't exist | `grep '"<word>"' internal/sqlparser/keywords_gen.go` — many keywords carry `_P` (`IN_P`, `TYPE_P`, `DATA_P`, `IDENTITY_P`, `ADD_P`) but not all (`REPLICA`, `MATERIALIZED`, `CREATE` have no suffix). Always grep first. |
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

1. **Probe the legacy parser first.** Never guess AST shapes or enum values.

   ```go
   // internal/sqlparser/zz_probe_test.go (temporary — delete when done)
   func TestProbe(t *testing.T) {
       parser.RouteBatch = RouteBatch        // MANDATORY: postmaster init is
       defer func() { parser.RouteBatch = nil }() // NOT linked into test binaries
       sts, _ := parser.Parse("ALTER TABLE t ADD COLUMN c int")
       fmt.Println(dumpStmts(sts))           // or print specific fields
   }
   ```

   Record: exact `Kind` enum values, which fields are populated, default
   values, position semantics.

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

6. **`make gen-parser`.** The conflict gate pins an exact count (currently 15)
   plus a token allowlist. If the count grew, find out why BEFORE bumping the
   pin (§6).

7. **Differential tests.** Move your probe cases into the permanent suite
   (`create_table_test.go` style: same SQL through both parsers, compare
   `dumpStmts`). Delete the `zz_` probe file.

8. **Flip routing** in `internal/sqlparser/dispatch.go`:
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
  └─ RouteBatch(src, toks)         // wired by postmaster init
       └─ routeBatch: SplitStatements → fragmentRouted per fragment
            ├─ first token "with"        → withFollowerRouted
            ├─ "create"/"drop"/"alter"   → secondKeywordRouted (pair map)
            └─ else                      → routedStmts
```

- If ANY fragment in a batch is not routed, the whole batch falls back to the
  legacy parser. Errors from the yacc parser (`handled=true, err!=nil`)
  surface directly — no silent fallback after routing.
- Unit tests must wire `parser.RouteBatch = RouteBatch` themselves (§5.1).

---

## 9. Test gates

- Fast iteration (~0.05 s): `go test ./internal/sqlparser/` —
  `TestLegacyCorpusParity` (floor is in the CODE — `corpus_parity_test.go`
  `legacyCorpusParityFloor`, currently **218** against a measured 223; this
  doc said "170" for a week because commit `2a55eff04`'s message claimed a
  floor bump it never made. Read the constant, never a doc), differential
  suites, `tpch_coverage_test.go` (floor 22/22).
- The floor is a **regression guard, not a target**: re-pin it to
  `measured − 5` in every wave that raises parity, and NEVER lower it to make
  a diff go away — document the diff in `difftest_known_diffs.md` instead.
- `diffParse` must call `ParseOneSrc`, never `ParseOne`. `ParseOne` leaves
  `lexerState.src` empty, so every raw-source span (`RawDef`, `CheckExpr`,
  SET values) silently compares as `""` on the yacc side and the harness
  cannot see that whole class of field.
- Planner/executor-touching changes additionally: `scripts/tpch-spotcheck.sh`
  (fresh capped server; canonical Q12/Q13 row counts).
- **Never pass `-count=1` to a gate** — it defeats the test-result cache.
- Live cluster hygiene: hold server age constant across A/B comparisons;
  `timeout N psql` kills only the client (the server keeps executing).

---

## 10. Git discipline

- Commit style: `area(scope): summary — detail`
  (e.g. `parser-rewrite(P4.2): ALTER TABLE multi-action comma lists`).
- Stage by explicit pathspec (`git add -A -- grammar/ internal/sqlparser/ ...`)
  — a concurrent Ralph loop's WIP may be present. Never bare `git add -A`.
- Never `git commit --no-verify` — the hook runs the pgbench smoke.
- Push `parse-refac` ONLY. `regress-renumbering` is the Ralph loop's working
  branch (M0134); the earlier wave pushed parser commits onto it, which mixes
  two unrelated tracks.

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
- Unit test parses via the legacy parser even though the class is flipped →
  forgot `parser.RouteBatch = RouteBatch` in the test.
- Server ignores new grammar after restart → stale server: `setup_goopg.sh`
  skipped the start. Stop first; verify the port is free (`ss -ltn`).
- "no partition of relation found" on INSERT into a partitioned parent →
  pre-existing EXECUTOR gap, not a parser bug (parser output verified
  identical to legacy).
