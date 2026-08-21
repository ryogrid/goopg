Task: M0134-0070 (strings.sql) — regress-sql `failed`. This loop implemented
the `pg_input_is_valid`/`pg_input_error_info` bytea case (3rd CONTAINED
bucket from the 2026-08-22 sizing pass; 1st was to_bin/to_oct, 2nd was
get_bit/set_bit/get_byte/set_byte). Committed/pushed: code `121a72b2`,
bookkeeping commit pending this loop's end.

Landed: `internal/executor/expr.go`'s inline `pg_input_is_valid` switch and
`internal/executor/operators_pg_input_error_info.go`'s `pg_input_error_info`
SRF switch both lacked a `case "bytea":` and fell through to the varchar
`default:`, so malformed bytea literals were always wrongly reported valid
with all-NULL error columns. Both now call the existing `byteaIn` parser
(`bytea.go`, mirrors PG oracle `byteain()` in `varlena.c`) and surface its
`*ExecError` Message/Code verbatim (`22023` odd-hex-digits/bad-hex-digit,
`22P02` invalid-escape-syntax) — pure reuse, no new mechanism, no wire-type
stamp needed (unlike the last 2 buckets) since these functions return
text/record columns unaffected by bytea width. New test
`internal/executor/bytea_input_error_info_test.go`. `strings.sql` diff
shrank **728→691 lines** (grep-confirmed zero residual pg_input_* lines).
Case still `failed`.

No new deferral-ledger row this loop — clean reuse fix, no new PG-semantics
gap discovered (unlike prior loops' sha/GUC/lexing discoveries).

Key symbols: `internal/executor/expr.go` inline `pg_input_is_valid` block
(~line 10336, `case "bytea":`); `internal/executor/
operators_pg_input_error_info.go`'s SRF switch (~line 168, `case "bytea":`);
both call `internal/executor/bytea.go`'s `byteaIn`.

Next step: re-check the fix_plan banner at loop start first (M-NIGHTLY
filing unconditional). Run 20260822-001356's 5 items are already filed as
of this loop, nothing new expected next loop unless a fresh nightly run
lands. Then continue M0134-0070. Remaining CONTAINED buckets from the
2026-08-22 sizing pass (re-verify sizes against the fresh 691-line diff
before picking, per this loop's own experience — sizes have shifted after
each fix):
(1) bytea↔int2/int4/int8 casts (~138 lines as of the 728-line diff, likely
still the biggest CONTAINED bucket) — needs a NEW width-disambiguation
mechanism on CastExpr since bytea casts don't carry the source-int-width
info `evalCast` needs, similar in shape to the to_hex/to_bin/to_oct
`ArgWidth` plan-stamp precedent already landed twice this pass — bigger
lift, consider whether it's still CONTAINED-sized or needs decomposing
further;
(2) sha224/sha384 missing entirely + sha256/sha512 wrongly return TEXT
instead of BYTEA (~63 lines as of the 728-line diff) — confirmed by this
loop's sizing researcher to ALSO need new `exprType` wire-type stamps for
`sha224`/`sha256`/`sha384`/`sha512`→bytea (same trap as get_bit/get_byte
last loop — brief must include the planner-side fix explicitly, do not
let a researcher call it "no wire-type change needed" again without
independent verification).
Larger/REFACTOR-tier buckets (do not brief as a single implementer round):
`standard_conforming_strings=off` lexing mode (~50-68 lines depending on how
counted), `escape_string_warning` GUC no-op registration (~13 lines,
trivial but its sibling lexing-mode bucket is NOT — do not conflate when
briefing), UESCAPE diagnostics residuals, RE2-vs-ARE regex-engine gaps
(multiple small residuals, already ledger-tracked), the ascii()/
bit_count()/psql-column-width wire-trace investigation (needs protocol-
logging not a normal round, already ledger-tracked), pg-oracle-diff.sh
--auto-start's `initdb -q` breakage (infra, unrelated to strings.sql
content). Re-run `GOOPG_CG_UNIT=... scripts/pg-regress-runner.sh --verbose
strings` before picking to confirm current line count and bucket sizes.

Gates run this loop: `go build ./...` PASS (coordinator re-verified,
independent of worker report); `go test ./internal/executor/... -run
'TestByteaInPgInputErrorInfoCases|TestPgInputErrorInfo|TestPgInputIsValid'
-v` PASS (coordinator spot-check, non-cached); `GOOPG_CG_UNIT=...
scripts/pg-regress-runner.sh --verbose strings` 728→691 lines (worker-run,
coordinator independently re-confirmed via `wc -l tmp/regress-diffs/
strings.diff`); pre-commit pgbench smoke via git hook — PASS (383/701/13084
TPS-equivalent triad, 0 failed). `make ralph-state-guard` — to run before
status block.

Delegation: researcher round (sizing bucket 3 vs the fresh 728-line diff,
inline, no handoff dir) DONE — confirmed 26-line bucket, recommended
pg_input_is_valid/pg_input_error_info as lowest-risk pick over the GUC
bucket. implementer round
(`tmp/ralph-handoffs/m0134-0070-pg-input-error-info-bytea/`) DONE in one
round — same recurring tooling friction as recent prior loops (blocked from
writing report.md by its own tool policy, relayed full report text in its
final message instead). Coordinator persisted the fix_plan note from the
relayed report and independently re-ran build/tests/regress-runner before
committing. No open handoff — brief.md remains on disk as a completed
record, no report.md file.

In-flight: none. Commit `121a72b2` (code) landed and pushed to
`regress-renumbering`; this loop's bookkeeping commit (fix_plan.md +
working_set.md) still pending as of this baton write — will be committed
and pushed before loop end. No server left running.
