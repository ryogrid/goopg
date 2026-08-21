Task: M0134-0070 (strings.sql) — regress-sql `failed`. This loop closed the
`reverse(bytea)` + `split_part()` bucket (researcher sizing round found both
CONTAINED, single-`case`-block fixes).
Committed/pushed: code `bc7402f3`, bookkeeping `7ab5e89b`
(`3f77636d..bc7402f3..7ab5e89b` on `regress-renumbering`).

Landed: `reverse()` (`internal/executor/expr.go`) now branches on
`Kind==KindBytes` to byte-reverse via `NewBytesDatum` (matches PG's
`bytea_reverse`, `varlena.c:3458-3474`), was unconditionally rune-reversing
`s.StringValue()` producing U+FFFD garbage on non-UTF8 bytea payloads.
`split_part()` rewritten to match PG's `split_part` (`varlena.c:4621-4750`)
exactly: `fldnum==0` raises `22023` "field position must not be zero",
negative field indices count from the end, empty delimiter returns the
whole string for field ±1 / empty otherwise (was Go per-rune
`strings.Split(s, "")`). `exprType()` (`internal/optimizer/planner.go`)
gained a matching `reverse` bytea/text wire-type case (same pattern as the
prior loop's btrim/ltrim/rtrim fix — without it the wire layer advertised
`text`/`unknown` for `reverse(bytea)` and rendered the byte-correct result as
mangled UTF-8; this was flagged as a scope deviation by the implementer
since the brief said "no dispatch-table changes" but was judged necessary to
satisfy the brief's own acceptance criterion, and is directly precedented by
the same-milestone `btrim`/`ltrim`/`rtrim`/`substr`/`overlay` arms already
in that switch). New test `internal/executor/reverse_splitpart_test.go` (15
subtests). `strings.sql` diff shrank **1029→952 lines**. Case still
`failed`.

Deferred (ledger row appended 2026-08-22): `to_hex(int)` on negative
arguments (`internal/executor/expr.go` ~13648-13655,
`fmt.Sprintf("%x", v.Int)`) prints a signed hex string (e.g. `-4d2`) instead
of PG's unsigned two's-complement hex (`fffffb2e` int4 / `fffffffffffffb2e`
int8) — needs the arg's declared width to pick the mask; ~4-line bucket, not
fixed this round.

Key symbols: `case "reverse":`, `case "split_part":`
(`internal/executor/expr.go`, ~line 12013-12028 and ~12166 pre-this-loop
offsets — re-grep, line numbers shift); `exprType()`'s FuncCall switch
(`internal/optimizer/planner.go`, ~line 12564).

Next step: re-check the fix_plan banner at loop start first (M-NIGHTLY
filing unconditional; 5 items already filed from the 20260822-001356 run,
none block M0134's gates, none selected — re-verify no NEW nightly run
landed since). Then continue sizing the 952-line `strings.sql` diff.
Candidates named this loop: `to_hex` negative-int two's-complement fix
(small/contained, same shape as this loop — recommended next), OR open the
dedicated `ascii()`/`bit_count()` wire-trace investigation slice (needs a
protocol-logging patch or packet capture vs a live PG 18.3 server for the
identical query — larger/riskier than a normal implementer round, consider
whether it needs a researcher round with Bash access to a running
goopg+PG pair rather than static reading). Remaining known buckets in the
952-line diff: the deferred `ascii()`/`bit_count()` spacing bucket,
obsolete-SQL99 `SUBSTRING(... SIMILAR ... ESCAPE ...)`, residual
Unicode-escape parser error-message/DETAIL mismatches, and now `to_hex`
negative-int. Also still open, no urgency: `scripts/pg-oracle-diff.sh
--auto-start`'s `initdb -q` breakage (ledger row dated 2026-08-22, M0134-0070
infra entry).

Gates run this loop: `go build ./...` PASS; `go test
./internal/executor/... ./internal/optimizer/...` PASS (cached, both
implementer + this session); `go test ./internal/executor/ -run
'TestReverseByteaByteReversal|TestSplitPartPGSemantics' -v` PASS all 15
subtests (implementer + re-verified this session); `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS (implementer + this session); live
psql spot-check via cgroup-capped throwaway server + PG oracle (implementer)
— all 4 brief acceptance-criteria queries byte-exact;
`scripts/pg-regress-runner.sh --verbose strings` 1029→952 lines
(implementer); pre-commit pgbench smoke via git hook — PASS twice (code
commit: 372/694/13036 TPS; bookkeeping commit: 376/693/13073 TPS, 0 failed
both times). `make ralph-state-guard` — ran this session, found the
running/completed status/progress mismatch from a normal prior-loop
clean-exit marker, self-repaired to consistent (status="running",
progress="in_progress"), final check OK.

Delegation: researcher round (sizing reverse(bytea) + finding split_part as
a second bucket, no handoff dir — inline in this conversation) DONE.
implementer round (tmp/ralph-handoffs/m0134-0070-reverse-splitpart/), DONE
in round 1, converged. report.md write was blocked by the implementer's own
tooling guard again (same recurring friction as prior loops); coordinator
wrote report.md manually into the handoff dir from the relayed agent output
— durable trail intact. No open handoff.

In-flight: none. Commits `bc7402f3` (code) and `7ab5e89b` (bookkeeping)
landed and pushed to `regress-renumbering` (`3f77636d..bc7402f3..7ab5e89b`).
No server left running.
