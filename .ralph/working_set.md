Task: M0134-0070 (strings.sql) — regress-sql `failed`. This loop implemented
`get_bit`/`set_bit`/`get_byte`/`set_byte` (bytea builtins), the second
CONTAINED bucket from the 2026-08-22 sizing pass (first was to_bin/to_oct).
Committed/pushed: code `e93800b1`, bookkeeping commit pending this loop's end
(`0a04e518..a4e7b471..53b20310..9636bea9..e93800b1` on `regress-renumbering`).

Landed: `internal/executor/expr.go` gained `case "get_byte":`/`"set_byte":`/
`"get_bit":`/`"set_bit":` next to the `to_hex`/`to_bin`/`to_oct` block,
matching PG oracle `byteaGetByte`/`byteaSetByte`/`byteaGetBit`/`byteaSetBit`
(`varlena.c:3310-3448`) — LSB-first bit numbering (bitNo=n%8 against the
byte's low bit), SQLSTATE `2202E` out-of-range errors with PG's exact text,
`set_bit`'s `22023` invalid-new-bit-value check, no `Pos` set. Also required
`internal/optimizer/planner.go`'s `exprType` to gain `set_byte`/`set_bit`→
bytea and `get_byte`/`get_bit`→int4 wire-type stamps — the sizing pass had
called this bucket "no wire-type change needed" but that was wrong; without
the stamp the wire renderer mis-serialized the byte-correct `KindBytes`
result (same M0125-0021-class gap already fixed for decode/substr/overlay/
btrim). New test `internal/executor/get_bit_set_bit_test.go`. `strings.sql`
diff shrank **783→728 lines** (grep-confirmed zero residual to_bin/to_oct...
er, get_bit/set_bit/get_byte/set_byte lines). Case still `failed`.

No new deferral-ledger row this loop — the worker's discovered pre-existing
gaps (psql column-width padding, standard_conforming_strings=off lexing,
int::bytea cast) are all duplicates of already-tracked rows (M0125-0021 for
the int::bytea cast; the chr/bytea-trim ledger row for psql padding; this
loop's own to_bin/to_oct ledger row for standard_conforming_strings=off).

Key symbols: `internal/executor/expr.go` `case "get_byte"/"set_byte"/
"get_bit"/"set_bit"` (next to `case "to_hex"`); `internal/optimizer/
planner.go` `exprType`'s `case "set_byte", "set_bit"`/`case "get_byte",
"get_bit"` (next to the `decode`/`encode` cases).

Next step: re-check the fix_plan banner at loop start first (M-NIGHTLY
filing unconditional — the run 20260822-001356 5 items are already filed,
nothing new as of this loop). Then continue M0134-0070. Remaining CONTAINED
buckets from the 2026-08-22 sizing pass, re-verify sizes against the fresh
728-line diff before picking (fixes may shift residuals):
(1) bytea↔int2/int4/int8 casts (~138 lines, needs a NEW width-disambiguation
mechanism on CastExpr since bytea casts don't carry the source-int-width
info `evalCast` needs — bigger lift, may need its own ArgWidth-style plan
stamp like to_hex/to_bin/to_oct got);
(2) sha224/sha384 missing entirely + sha256/sha512 wrongly return TEXT
instead of BYTEA (~60 lines);
(3) pg_input_is_valid/pg_input_error_info wrong for bytea (~30 lines);
(4) escape_string_warning GUC no-op registration (~28 lines, trivial — but
note the REFACTOR-tier standard_conforming_strings=off lexing mode is a
separate, much bigger sibling bucket from the SAME ledger row, do not
conflate the two when briefing).
Larger/REFACTOR-tier buckets (do not brief as a single implementer round):
UESCAPE diagnostics (~40 of ~104), standard_conforming_strings=off lexing
mode (~68), RE2-vs-ARE regex-engine gaps (multiple small residuals),
char(20) 'literal' typed-string-literal syntax (~16), the ascii()/
bit_count() wire-trace investigation (~40, needs protocol-logging not a
normal round), pg-oracle-diff.sh --auto-start's `initdb -q` breakage
(infra, unrelated to strings.sql content). Re-run
`scripts/pg-regress-runner.sh --verbose strings` before picking to confirm
current line count and bucket sizes.

Gates run this loop: `go build ./...` PASS (independently re-verified by
coordinator, not just trusted worker report); `go test ./internal/
executor/... ./internal/optimizer/...` PASS (coordinator re-ran, cached);
`go test -run TestGetBit ./internal/executor/... -v` PASS all 9 subtests
(coordinator spot-check, non-cached); `GOOPG_CG_UNIT=...
scripts/pg-regress-runner.sh --verbose strings` 783→728 lines (worker-run,
grep-verified no residual get_bit/set_bit/get_byte/set_byte lines);
pre-commit pgbench smoke via git hook — PASS (378/698/13060 TPS-equivalent
triad, 0 failed). `make ralph-state-guard` — to run before status block.

Delegation: researcher round (sizing the get_bit/set_bit/get_byte/set_byte
bucket, inline, no handoff dir) DONE — confirmed 54-line bucket, all-4-
missing, self-contained-executor-only assessment (later proven partially
wrong re: planner wire-type stamp). implementer round
(`tmp/ralph-handoffs/m0134-0070-get-bit-set-bit-get-byte-set-byte/`) DONE in
one round — same recurring tooling friction as recent prior loops (could not
write report.md via its own tools; relayed full report text in its final
message, including a correctly-flagged deviation for the planner change).
Coordinator persisted the fix_plan note from the relayed report and
independently re-ran build/tests before committing. No open handoff —
brief.md remains on disk as a completed record, no report.md file.

In-flight: none. Commit `e93800b1` (code) landed and pushed to
`regress-renumbering`; this loop's bookkeeping commit (fix_plan.md +
working_set.md) still pending as of this baton write — will be committed
and pushed before loop end. No server left running.
