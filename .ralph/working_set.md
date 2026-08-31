(idle — nothing in flight)

## Loop #5 (2026-09-01) result — nightly filing (no-op, same run) +
## M0134-0184 (unicode.sql) CLOSED

**Nightly triage:** `ci/logs/action-items.md` still shows run `20260901-010436`
(same sha as loop #4) — already filed last loop
(`.ralph/fix_plan.md:1582` "Nightly run 20260901-010436" section). No new
items to file this loop.

**Task:** M0134-0184 — `unicode.sql`. **CLOSED** (CSV `not-tried` → `pass`,
`pass_required=yes`). 23 diff lines → 0, 100% parity. Design
`docs/design/0100-0149/m0134-0184-unicode-normalization.md`.

**Shipped:** `normalize()`/`is_normalized()`/`unicode_assigned()`/
`unicode_version()`/`IS [NOT] [form] NORMALIZED` were entirely unimplemented
(also stale-excluded by policy as "out of scope" in `regressExcluded`
(`internal/testport/regress_suite_test.go`) and its required-sync copy in
`cmd/gen-regress-coverage/main.go` — both entries removed). `normalize`/
`is_normalized` use `golang.org/x/text/unicode/norm` (already a `go.mod`
dep, used by `saslprep.go`) for NFC/NFD/NFKC/NFKD; `unicode_assigned` uses
stdlib `unicode.Cn` — no UCD data embedding needed (this was the one
plausible PARK reason and it turned out to be a non-issue). Grammar
(`grammar/pg_grammar.y`): new `unicode_normal_form` nonterminal +
`NORMALIZE(...)` productions in `func_expr_common_subexpr` + 4
`a_expr IS [NOT] [form] NORMALIZED` productions next to `IS DISTINCT FROM`,
all via the existing `specialFormCall` helper (same pattern as
OVERLAY/TRIM) — no new synthetic terminals, all keyword tokens were already
lexed but unconsumed. Conflict pin bumped 59→60 in `Makefile` (one new `'('`
shift/reduce: `NORMALIZE` is `CatColName` so stays usable as a bare ColId,
same class as EXTRACT/TRIM/OVERLAY). One gotcha caught by the live diff: the
first `22023 invalid normalization form` implementation set `Pos: x.Pos()`
like most other 22023s in the file, but PG's `unicode_norm_form_from_string`
never calls `errposition()` so the real output has no `LINE n:` caret —
fixed by omitting `Pos`.

**Golden-corpus gotcha (learned this loop, worth remembering):**
`GOOPG_UPDATE_GOLDENS=1 go test <filtered -run>` DESTRUCTIVELY overwrites
`parity_goldens.txt` with only the queries that ran in that invocation —
`TestMain` writes the file after every test in the package, so a filtered
run drops every other test's golden. First attempt filtered to my new test
and turned a 1667-line file into 13 lines; caught via `git diff --stat`
before committing, reverted with `git checkout --`, redid with
`GOOPG_UPDATE_GOLDENS=1 go test ./internal/parser/` (no `-run`, full
package) → clean additive 13-line diff. Always regenerate goldens over the
WHOLE package, never a `-run`-filtered subset.

**Gates run:** `go build ./...` clean; `make gen-parser` clean (conflict
count 60, pin updated); `go test ./internal/parser/...` full package PASS
(includes new `TestParseNormalizeFuncCall`/`TestParseNormalizeFuncCallWithForm`/
`TestParseIsNormalized`/`TestParseUnicodeNormalizeParity`);
`go test ./internal/executor/...` full package PASS;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` full units
suite PASS; `scripts/tpch-spotcheck.sh` PASS (Q12 rows=2, Q13 rows=34,
27.0s); `scripts/pg-regress-runner.sh -v unicode` 1/1 PASS 100% parity;
`go test -v -run '^TestPort_RegressSuite$/unicode$' ./internal/testport/`
PASS; `make check-testport-inventory` PASS; `make regen-testport` clean
5-file regen; `make ralph-state-guard` PASS (one auto-repair: progress.json
stale "completed" marker reconciled to in_progress — same benign pattern as
loop #4, pre-existing, not caused by this loop); pre-commit pgbench smoke
PASS (504/646/11693 TPS, 0 failed). No deferral ledger row — nothing was
shortcut for this case.

**NEXT LOOP:** Re-check banner (M0134 priority as of writing). Next
unclaimed M0134 case per ordering is **M0134-0185** (`vacuum_parallel.sql`,
`not-tried`, never sized) — pick that up unless the banner changes. The two
M-NIGHTLY `pg_stat_activity` failures filed loop #4
(AI-20260901-010436-005/-007) remain untriaged — not selected this loop
either since M0134 stays next-priority per banner.

**In-flight:** none.
