Task just completed: **M0134-0158 — publication.sql**, PARKED (`not-tried` →
`failed`, 2360 → 2233 lines, `^+ERROR` 171 → 111) — but the loop's real output
is TWO CROSS-CUTTING PARSER FIXES, not publication work.

**The discovery:** 69 of the 171 goopg-side errors (40%) were the identical
`syntax error at or near "OPERATOR"` — on SQL **psql generates**, not SQL the
test writes. `processSQLNamePattern` (`fe_utils/string_utils.c:1121-1152`)
appends `<namevar> OPERATOR(pg_catalog.~) '^(pat)$' COLLATE pg_catalog.default`
to EVERY describe meta-command given a pattern, so this was every
`\d name` / `\dt pat` / `\di pat` / `\dv pat` in the WHOLE regress corpus.
Two independent gaps, neither observable alone:
1. `qual_Op` (`gram.y:16658`) was never ported — goopg had only the bare-`Op`
   half of all four rules that use it (`:15009`, `:15011`, `:15488`, `:15490`).
   Fixed with a NEW additive `qual_op` nonterminal (parenthesised half only, so
   the four proven bare-`Op` rules are untouched), reusing
   `any_operator_name`/`op_run`. `%prec Op` mandatory — bodies end in a
   nonterminal. Qualifier is DROPPED (goopg has no schema-qualified operator
   node); upstream resolves it (`LookupOperName`, `parse_oper.c:99`) — ledgered.
2. `COLLATE` took `ColId '.' ColId`; upstream takes `any_name` whose tail is
   `attr_name: ColLabel` (`:14867`, `:9161`, `:17724`). `default` is RESERVED,
   so `pg_catalog.default` — the only qualified collation psql writes — was
   unreachable. Now `ColId '.' as_col_label`.

Files: grammar/pg_grammar.y, internal/parser/support.go (`qualOpName`,
`qualPrefixExpr`), internal/parser/qual_op_test.go (new, 4 guards),
regenerated yacc_parser.go/tokennums_gen.go/parity_goldens.txt,
docs/design/m0134-0158-qualified-operator-and-collate-any-name.md (new) +
README index, CSV + regen-testport, fix_plan (+0158a/0158b), ledger (4 rows).

Gates run: `make gen-parser` conflict pin held at **exactly 59** on the
unchanged allowlist; goldens **22 insertions / 1 deletion** (deletion = count
header, so ZERO existing pins moved = purely additive); `go test
./internal/parser/` PASS; **13-file before/after regress sweep** (HEAD worktree
baseline, now removed) net **−1848 lines, ZERO regressions** — constraints
745→167, alter_table 4247→3776, inherit 3689→3250; create_view (+7) and
dependency (+2) grew only because a 3-line syntax error became a longer
executing-but-diverging table; exactly one new `+ERROR` class corpus-wide
(`pg_relation_is_publishable`, reachable only now). `RALPH_PRECOMMIT_SCOPE=units`
exit 0. **Honest note:** the FIRST units run ended `FAIL`; two subsequent full
runs exited 0 with every package green, and a `-count=1` probe of
postmaster/executor/optimizer passed — one-off, package never identified
(only had `tail -25` of run 1). Concurrent nightly was loading the host.

In-flight: none. Baseline worktree /tmp/goopg-qualop-base removed.

**Carried obligations (3rd loop running; nightly 20260828-235424 STILL live —
at the TPC-H stage at 02:32, TPC-DS stage still to come):**
1. **TPC-DS SF0.5 gate still NOT run** (for -0156, -0157; -0158 is
   parser-only + all 1639 pre-existing goldens byte-identical, so it cannot
   move a TPC-DS plan). Once the nightly finishes:
   `FORCE=1 GOOPG_BIN=$PWD/tmp/goopg-sf05-bin scripts/tpcds-sf05-regression.sh sweep`.
2. **The 110 "presumed stale, pending" 20260827 rows are STILL unadjudicated.**
   Run 20260828-235424's testport stage **hit its 120m timeout and FAILed
   (rc=1) at 02:02 with no results.csv** — 2nd consecutive wedge. This is the
   known `TestPort_IsolationSuite` full-run wedge (playbook §9: run it
   `-run TestPort_IsolationSuite` FIRST, then the rest — never both at once);
   the nightly stage runs the full suite in ONE go, so it will wedge every
   night until `ci/batch/stages/stage-testport.sh` is split. Worth filing when
   a `## AI-` item for it appears.

NEXT LOOP: file any new `## AI-` items (action-items.md is still the 20260827
file, all 133 already filed), then per the Current Priority banner work
**M0134-0159 (regproc.sql)**. Short-loop alternatives now filed: M0134-0158a
(publication grammar subset + `pg_relation_is_publishable`), M0134-0158b (`~~`
family — note the BARE path MISPARSES `'a' ~~ 'b'` as `'a' ~ (~ 'b')`),
M0134-0157a, M0134-0157b.
