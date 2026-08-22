Task: M0134-0070 (strings.sql) — regress-sql `failed`. This loop landed the
`lpad`/`rpad` empty-fill value bug (the recorded "smallest remaining CONTAINED
value-bug, ~6 lines"). Code commit `db647aad` (+ bookkeeping), pushed.

Landed: `padLeft`/`padRight` (`internal/executor/expr.go:15049-15051/15077-15079`)
substituted a space for an explicitly-empty third argument (`if fill == "" {
fill = " " }`), so `lpad('hi',5,'')` returned `'   hi'` and `lpad('hi',1,'')`
returned `' '`. PG (`oracle_compat.c:196-197/294-295`) sets `len=s1len` when
`s2len<=0` — no padding, but truncation (runs BEFORE the empty-fill check) still
applies. Fixed both siblings to `if fill == "" { return s }`; the pre-existing
`len(runes) >= n` early return already does truncation; the 2-arg call-site
default `fill := " "` (PG's separate `lpad(text,int)` overload) is untouched.
New test `internal/executor/pad_empty_fill_test.go` (8 subtests). `strings.sql`
diff shrank 395→367 lines (zero residual lpad/rpad lines).

Key symbols: `internal/executor/expr.go` `padLeft` (~15041) / `padRight` (~15069)
(the twin pair), call sites `case "lpad":` (~12129) / `case "rpad":` (~12146).

Hypothesis/Findings: the oracle order matters — `if (s1len > len) s1len = len;`
(truncate) precedes `if (s2len <= 0) len = s1len;` (no-pad), so `lpad('hi',1,'')`
→ `'h'` not `'hi'`. goopg's early-truncation return already mirrors this order,
so `return s` is only reachable when no truncation occurred.

Next step: re-triage the fresh 367-line `strings.sql` diff to size the next
smallest CONTAINED bucket — candidates: `bytea LIKE` (~6 lines), regexp
error-path HINT (~7), toasttest `pg_relation_size` NULL (~2). Then brief +
implement that bucket. (The `standard_conforming_strings=off` + `escape_string_warning`
bucket is REFACTOR-tier and stays deferred/ledgered.)

Gates run this loop: `go build ./...` PASS; `go test ./internal/executor/ -run
TestPad -v` PASS (8/8 + pre-existing pad tests); `go test ./internal/executor/`
PASS; `scripts/pg-regress-runner.sh --verbose strings` 395→367 lines; pre-commit
pgbench smoke PASS (11972 TPS, 0 failed) via git hook. `make ralph-state-guard`
— run before status block.

Delegation: implementer (m0134-0070-lpad-rpad-empty-fill) DONE — all gates PASS,
committed as `db647aad`. Handoff dir `tmp/ralph-handoffs/m0134-0070-lpad-rpad-empty-fill/`.

In-flight: none.
