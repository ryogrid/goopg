Task just completed: M0134-0102 (collate.utf8.sql) — sized live against the
PG 18.3 oracle: shipped one contained fix, PARKED (case stays `failed`,
not a clean close).

Landed: `execCreateCollation`'s `provider = builtin` branch
(internal/executor/operators_ddl.go) now ports PG's `builtin_validate_locale`
(pg_locale.c:1510, called from DefineCollation before the pg_collation
insert): only `C`, `C.UTF-8`/`C.UTF8` (canonicalized to `C.UTF-8`), and
`PG_UNICODE_FAST` are accepted; anything else (e.g. `'unicode'`, `'C_UTF8'`)
now raises 22023 "invalid locale name ... for builtin provider" instead of
silently registering a bogus collation. Before this fix the file's two
negative tests (`locale = 'unicode'` / `'C_UTF8'`, both commented `-- fails`)
silently succeeded, so the immediately-following correctly-spelled `CREATE
COLLATION` of the SAME name then collided with a spurious "already exists" —
cascading a false diff through the rest of the file. New test
internal/executor/create_collation_builtin_locale_test.go (3 subtests, all
pass): invalid-name rejection + catalog-stays-clean, underscore-spelled
rejection, and all 3 canonical spellings accepted with correct
canonicalization.

Six more independent root causes sized and ledgered, NOT fixed this loop
(largest first): (1) no builtin-provider Unicode case-mapping engine —
lower()/upper()/initcap() in internal/executor/expr.go ignore the argument's
declared collation entirely (always full-Unicode strings.ToUpper/ToLower
regardless of C-locale-should-be-ASCII-only vs PG_UNICODE_FAST-should-do-
full-mapping) — same "no collation execution engine" class as M0134-0099/
-0100/-0101; (2) Final_Sigma context rule for PG_UNICODE_FAST's lower()
unimplemented (goopg always emits plain ς, never word-final ς); (3)
casefold() SQL function does not exist at all — no pg_proc row, no handler;
(4) convert_to()/convert_from() have a pg_proc row (pg_proc_seed_data.go:1058,
HandlerName "pg_convert_to") but NO Go implementation anywhere — pure
metadata; (5) SYSTEMIC BUG, broader than this file: length/char_length/
octet_length/bit_length/upper/lower/etc. in internal/executor/expr.go all do
`s, err := evalExprSlot(...); if err != nil || s.IsNull() { return
NullDatum, nil }` — a genuine evaluation error (e.g. "function does not
exist") is silently swallowed into NULL instead of propagating 42883. Live
repro: `SELECT octet_length(nonexistent_func_xyz('x'))` returns NULL, no
error — while the bare call or under `||` correctly raises 42883. This
masked bucket (4)'s missing-function error inside `length(convert_to(t,
'UTF8'))`, producing blank byte-count columns instead of an ERROR line, in
BOTH of the file's per-provider result tables. Flagged as independently
high-value: needs its own dedicated loop (audit every `err != nil ||`
occurrence in expr.go — dozens expected — split into proper error-propagate
vs NULL-propagate branches, then re-run the full regress-port suite per
Hard-won Rule #5); (6) initcap() fullwidth-digit edge case, not yet isolated
to a minimal repro.

Design doc: docs/design/m0134-0102-builtin-collation-locale-validation.md
(full detail + PG citations for the fix and all 6 remaining buckets).
README.md indexed. Ledger row appended: .ralph/deferral_ledger.md,
2026-08-24, M0134-0102.
CSV row flipped not-tried -> failed via manual edit + `make regen-testport`.
fix_plan.md M0134-0102 marked [x] with PARKED note.

NOT YET COMMITTED this loop — commit is the immediate next action (staged
list below), before starting M0134-0103.

Nightly filing: checked ci/logs/action-items.md at loop start — same run
(20260824-013441, sha e7495e712dda) as prior 8 loops, already filed
(AI-...-001 units/internal/executor regression, AI-...-002 AdvisoryLock
repeat). No new filing needed this loop.

NEXT LOOP: first, confirm this loop's commit landed
(`git log --oneline -1 origin/regress-renumbering`). Then per the Current
Priority banner in .ralph/fix_plan.md, continue M0134 top-to-bottom — next
unworked item is **M0134-0103 (collate.windows.win1252.sql)**. Check for a
`\if :skip_test` self-skip guard FIRST via a quick grep of the .sql file
before sizing live (the "windows.win1252" name suggests a Windows-1252
encoding-family guard similar to -0099/-0100's provider-availability guards
— likely another automatic PARK). If bandwidth allows a cross-cutting
detour: bucket (5) above (systemic err-swallow-to-NULL bug in expr.go's
string builtins) is the highest-value opportunistic pickup found so far —
broader than any single collate.*.sql file — but is deliberately NOT
scoped for a quick pickup (blast radius needs its own dedicated
verification pass across the whole regress-port suite). Standing
recommendation from the M0134-0095/-0096/-0097 BRIN PARKs still applies if a
loop has implementation bandwidth to spare: brin_summarize_range/
brin_desummarize_range are unimplemented and block 3 files at once — but the
banner's straight top-to-bottom order continues to win absent an explicit
re-prioritization.

Gates run: go build ./... clean; go test ./internal/executor/ -run
TestCreateCollationBuiltinLocaleValidation -v PASS (3/3 new subtests);
RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh PASS (all unit
packages, incl. internal/executor 7.4s, internal/initdb 434s — cold cache
after branch/toolchain state, not a regression); make check-testport-inventory
PASS; make regen-testport clean; make ralph-state-guard PASS (auto-repaired a
stale status/progress running-vs-completed mismatch from the prior loop, then
confirmed consistent). Pre-commit hook's pgbench smoke will run automatically
at commit time (mandatory, never bypassed) — not yet executed as of this
writing since the commit is still pending.

In-flight: none. tmp/regress-diffs/ scratch output from the sizing run was
left in place (gitignored scratch dir, not committed) — safe to overwrite
next loop. A throwaway goopg server + data dir under /tmp/zzcollate (started
via `goopg-test-run.sh` for live sizing, port 5533) was cleanly stopped via
`goopg stop -D /tmp/zzcollate` before this loop ended — no orphan.
