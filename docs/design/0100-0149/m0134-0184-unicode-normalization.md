# M0134-0184 — `unicode.sql`: Unicode normalization functions implemented, CLOSED

Status: **CLOSED** — 100% parity (0 diff lines), CSV flipped to
`pass`/`pass_required=yes`.

## What the file tests

`postgres/src/test/regress/sql/unicode.sql` (32 lines) exercises PG's
Unicode-support builtins: `unicode_version()`, `unicode_assigned(text)`,
`normalize(text[, form])`, `is_normalized(text[, form])`, and the SQL-standard
predicate spelling `<string> IS [NOT] [form] NORMALIZED`, across all four
normalization forms (NFC/NFD/NFKC/NFKD) plus the two run-time-error cases
(an invalid form string).

## Sizing (this loop, 2026-09-01)

First live run: **0/1 PASS, 23 diff lines** (CSV was `not-tried`, and the
case was additionally stale-excluded by policy in
`internal/testport/regress_suite_test.go`'s `regressExcluded` map and its
`cmd/gen-regress-coverage` mirror — both said "Unicode normalization; out of
scope for goopg v0." even though none of these functions require anything
goopg didn't already have available). After the fix: **1/1 PASS, 0 diff
lines** via both `scripts/pg-regress-runner.sh -v unicode` and
`go test -run '^TestPort_RegressSuite$/unicode$' ./internal/testport/`.

### Root cause

None of `normalize`/`is_normalized`/`unicode_assigned`/`unicode_version`/
`IS NORMALIZED` had any implementation:

- **Catalog**: `internal/initdb/pg_proc_seed_data.go` already carried the
  `pg_proc` rows for the 2-arg overloads (oids 4549/6105/4350/4351, copied
  verbatim from `postgres/src/include/catalog/pg_proc.dat:12429-12447`) —
  the catalog side was never the gap.
- **Grammar**: `grammar/pg_grammar.y` lexed the keyword tokens
  (`NFC`/`NFD`/`NFKC`/`NFKD`/`NORMALIZE`/`NORMALIZED` were all already present
  in `grammar/tokens_gen.y`/`kwlists_gen.y`/`internal/parser/keywords_gen.go`,
  generated from `kwlist.h`) but **no production consumed them** — `grep -n
  "NORMALIZED\|NORMALIZE\b" grammar/pg_grammar.y` returned zero matches before
  this loop.
- **Executor**: `internal/executor/expr.go`'s `evalFuncCall` switch had no
  case for any of the four function names.

## What was implemented

Three surfaces, mirroring gram.y and goopg's existing idioms exactly:

1. **Grammar** (`grammar/pg_grammar.y`) — gram.y `:16789ff` /
   `:15364-15393` / `:15896-15912`:
   - `unicode_normal_form: NFC {"NFC"} | NFD {"NFD"} | NFKC {"NFKC"} | NFKD
     {"NFKD"}` — a new `%type <str>` nonterminal producing the uppercase form
     name as a plain string, fed as a `StringConst` argument (mirrors gram.y's
     `makeStringConst($N, @N)`).
   - `NORMALIZE '(' a_expr ')'` and `NORMALIZE '(' a_expr ','
     unicode_normal_form ')'` in `func_expr_common_subexpr`, next to the
     existing `OVERLAY`/`TRIM` special-form productions — both use
     `specialFormCall(pos, "normalize", args)`, the same
     `COERCE_SQL_SYNTAX`-equivalent helper OVERLAY/TRIM/`timezone()` already
     use (`internal/parser/support.go:1672`), so `Variadic` stays nil instead
     of the general `NewFuncCall` path's per-argument flag slice.
   - `a_expr IS [NOT] [unicode_normal_form] NORMALIZED %prec IS` (4
     alternatives) next to `IS DISTINCT FROM`, using the same
     `specialFormCall` for the underlying `is_normalized(...)` call and
     `NewUnaryOp(pos, OpNot, ...)` to wrap the two negated forms — gram.y's
     `makeNotExpr` equivalent (goopg has no dedicated NOT-expr node; unary NOT
     is how `NOT a_expr` itself is built at `pg_grammar.y:2387-2389`).
   - **No new synthetic terminals** were needed (unlike `TYPEDLIT`/`*_LA` —
     playbook §12.1): NFC/NFD/NFKC/NFKD/NORMALIZE/NORMALIZED are 1:1 with
     gram.y's own tokens, already lexed, just unconsumed.
   - Conflict pin bumped **59 → 60** in `Makefile` (`gen-parser` target): one
     new `'('` shift/reduce, because `NORMALIZE` is a `CatColName` keyword
     (stays usable as a bare `ColId`) so `NORMALIZE(` is ambiguous between a
     keyword-call and a ColId immediately followed by a parenthesized
     expression — the same class already covered by
     `EXISTS`/`EXTRACT`/`TRIM`/`SUBSTRING`/`OVERLAY`/`POSITION` etc. Doc
     string in the Makefile updated in the same edit (playbook rule: pin count
     and message must move together).

2. **Executor** (`internal/executor/expr.go`, `evalFuncCall`) — four new
   `case` arms:
   - `"normalize"` / `"is_normalized"` both default the form argument to
     `"NFC"` when called with 1 arg (PG achieves this via a
     `system_functions.sql:626-637` SQL-level `DEFAULT 'NFC'` wrapper
     function shadowing the 2-arg C builtin — goopg has no default-argument
     catalog mechanism, so the default is applied directly in the executor,
     the same way `btrim`/`ltrim`/`rtrim` above already default their cutset
     argument from `len(x.Args)`). Both use a new helper,
     `unicodeNormalizationForm(s string) (norm.Form, bool)`, mapping the
     case-insensitively-compared form string
     (PG: `unicode_norm_form_from_string`, `pg_strcasecmp`) to
     `golang.org/x/text/unicode/norm`'s `norm.NFC`/`NFD`/`NFKC`/`NFKD`. An
     unrecognized form raises `22023 invalid normalization form: %s` with
     **no `Pos`** — `unicode_norm_form_from_string` (varlena.c:6517-6541)
     never calls `errposition()`, so PG's error carries no `LINE n:` caret;
     the first implementation attempt set `Pos: x.Pos()` like most other
     22023s in this file and produced two spurious `LINE 1: ...` / `^` lines
     the live diff caught immediately.
   - `normalize` returns `form.String(s.StringValue())`;
     `is_normalized` returns `form.IsNormalString(s.StringValue())`.
   - `"unicode_assigned"` — for every rune in the input, `unicode.Is(
     unicode.Cn, r)` (Go stdlib `unicode` package; category `Cn` = "Other, not
     assigned" tracks the same UCD assignment split PG's own
     `unicode_category()`/`PG_U_UNASSIGNED` check does at
     varlena.c:6572-6600) — `false` on the first unassigned codepoint found,
     `true` otherwise. **No UCD data embedding was needed**: this was the one
     candidate blocker that could have forced a PARK, and Go's stdlib already
     ships the full assignment table.
   - `"unicode_version"` — hardcoded `"15.0"` (the file only asserts
     `IS NOT NULL`, matching x/text's bundled `norm.Version = "15.0.0"`; PG's
     own `PG_UNICODE_VERSION` is likewise a build-time constant with no
     asserted value in this test).
   - New import: `golang.org/x/text/unicode/norm` — already an explicit
     `go.mod` dependency (v0.21.0) and already imported elsewhere
     (`internal/libpq/auth/saslprep.go` uses `norm.NFKC.String(...)` for
     SASLprep), so this added no new module-cache/vendoring risk.

3. **Policy reversal**: `unicode` was marked `"Unicode normalization; out of
   scope for goopg v0."` in both `internal/testport/regress_suite_test.go`'s
   `regressExcluded` map and its required-sibling
   `cmd/gen-regress-coverage/main.go` copy (the file comment: "Must stay in
   sync with cmd/gen-regress-coverage"). Both entries removed in this loop so
   `TestPort_RegressSuite` actually executes the case instead of skipping it.

## Encoding-guard note

PG's `unicode_norm_form_from_string`/`unicode_assigned` both additionally
check `GetDatabaseEncoding() != PG_UTF8` and raise before even validating the
form. goopg has exactly one supported server encoding (UTF8), so this check
is unconditionally true and was omitted — there is no non-UTF8 goopg database
for it to ever fire against.

## Verification

- `go build ./...` clean.
- `make gen-parser` clean (conflict count 60, matches the updated pin).
- `scripts/pg-regress-runner.sh -v unicode`: 1/1 PASS, 100% parity, 0 diff
  lines.
- `go test -v -run '^TestPort_RegressSuite$/unicode$' ./internal/testport/`:
  PASS.
- `make check-testport-inventory` / `make regen-testport`: clean.

## No deferral ledger row

Every PG behavior this file exercises is now faithfully implemented — form
validation, case-insensitivity, the 1-arg NFC default, the `IS [NOT]
NORMALIZED` predicate rewrite, and the run-time-error message text/absence of
a position caret. Nothing was shortcut, so no `.ralph/deferral_ledger.md` row
applies to this task.
