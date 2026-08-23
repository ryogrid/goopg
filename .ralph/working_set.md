(idle — nothing in flight)

Task just completed: M0134-0092 (bit.sql) + nightly filing for run
20260824-013441 (both done this loop, one task/loop rule: filing is
unconditional per the M-NIGHTLY rule, not a second "task").

Nightly filing: added AI-20260824-013441-001 (units/internal/executor —
TestRecursiveUnionCapsRunawaySingleIteration nil-deref panic inside
recursiveUnionOp.Next at operators_recursive_cte.go:163, o.ctx.Ctx.Err()) as
a new fix_plan.md row under a new "Nightly run 20260824-013441" section.
Investigated it live this loop: could NOT reproduce — the single test alone,
the whole internal/executor package with -count=1, and 3x -count=1 -race
runs of the whole package all PASS clean, no race flagged. Left OPEN as a
suspected flake (host memory/GC pressure during the nightly's concurrent
ci/batch stages is the working theory, unconfirmed) — re-investigate only if
it recurs. AI-20260824-013441-002 (AdvisoryLock_SessionUnlockAcrossBeginBoundary)
is a repeat of the already-open AI-20260822-001356-003 row, updated with a
third-failure note.

M0134-0092 (bit.sql), sized live against the PG 18.3 oracle
(scripts/pg-regress-runner.sh --verbose bit): 1054-line diff, 0% parity.
File's first blocker was structural: parser had ZERO support for the SQL99
bit-string literal syntax (B'1010'/X'A') — every INSERT raised a bare
syntax error. Landed:
(1) internal/parser/token.go: new TokenBitStringLit kind.
(2) internal/parser/lexer.go: lexBitOrHexString (mirrors lexEscapeString's
    E'...' prefix-detection pattern in next()); Token.Value carries a
    leading 'b'/'x' marker byte ahead of raw digits.
(3) internal/parser/expr.go: decodeBitStringLit — validates digit set
    EAGERLY (binary 0/1, hex 0-9A-Fa-f; 22P02 + errposition at literal
    start on invalid digit, matching PG's unconditional bit_in() call in
    parse_node.c's T_BitString case), decodes hex nibbles MSB-first, wraps
    result in a plain *StringConst (NOT a dedicated bit-typed node —
    deliberate ledgered simplification: literal ends up typed UNKNOWN not
    BITOID; untyped/operator-dispatch contexts not PG-faithful, but
    bit.sql's literal cases don't exercise that gap).
(4) internal/parser/select.go: parsePrimary case TokenBitStringLit ->
    decodeBitStringLit(t).
Exposed a SECOND, independent, PRE-EXISTING gap while testing: bit(n)/
varbit(n) COLUMNS had zero length/digit coercion at all (reproduced even
with a plain quoted-string literal, no B prefix — confirmed via manual
psql: INSERT INTO t(b bit(11)) VALUES ('10') silently stored "10"
unvalidated). Fixed:
(5) internal/executor/codec.go: coerceTextLikeDatum gained "bit"/"varbit"
    arms + new validateBitDigits helper — bit(n) exact-length check (22026
    ERRCODE_STRING_DATA_LENGTH_MISMATCH), bit varying(n) upper-bound check
    (22001 ERRCODE_STRING_DATA_RIGHT_TRUNCATION "too long").
Verified live: BIT_TABLE/VARBIT_TABLE fixture + 4 invalid-digit SELECT
cases now byte-for-byte identical to oracle. Diff 1054 -> 581 lines (still
0% file parity — regress-runner is all-or-nothing per file). Landed
internal/parser/bit_string_literal_test.go (TestParseBitStringLiteral,
TestParseBitStringLiteralInvalidDigit) + internal/executor/
bit_string_literal_test.go (TestBitStringLiteralInsertRoundTrip,
TestBitStringLiteralHexDecode). CSV row not-tried -> failed (genuinely
still failing). Ledger row + design doc (docs/design/m0134-0092-bit-
string-literal-syntax-and-column-coercion.md) both record the FULL
remaining gap list: bitwise operator family (~ & | # << >>) entirely
missing for bit/varbit, || concat has no bit arm, length()/SUBSTRING()
don't recognize bit/varbit args, COPY hex-format decode doesn't run,
pg_input_is_valid/pg_input_error_info don't route bit validation. Case
PARKED (still `failed`) per established M0134 pattern.
Committed eac970d2 and pushed to origin/regress-renumbering (confirmed).
fix_plan.md M0134-0092 marked [x] (PARKED convention, matches M0134-0090).

NEXT LOOP: per the Current Priority banner in .ralph/fix_plan.md, continue
M0134 top-to-bottom — next unworked item is **M0134-0093 (bitmapops.sql,
status not-tried)**. First: `git log --oneline -1 origin/regress-renumbering`
to confirm eac970d2 landed (should already be true). Then size bitmapops.sql
live against ./postgres oracle via scripts/pg-regress-runner.sh --verbose
bitmapops (background, generous timeout; `rm -rf tmp/regress-goopg-data`
first if a prior run left it non-empty). CAUTION carried forward from
M0134-0086/0090/0092: watch `ps -o rss= -C goopg` while any regress file
runs (RSS has hit 20+ GB in <2 min on prior files); kill -KILL promptly
(never bare pkill -f) if RSS climbs unbounded before deciding whether it's
worth fixing first. If bitmapops.sql resolves to a small/contained diff,
land the fix + ledger + design doc + CSV flip in one loop (M0134's
established per-task pattern: PARK on a multi-root-cause case after landing
the one contained fix, CLOSE clean on a small one — bit.sql this loop
followed the PARK branch; async.sql M0134-0091 followed the CLOSE branch).
Also worth a quick look: since bit.sql exposed that PLAIN string literals
into bit(n)/varbit(n) columns were unvalidated pre-existing (not just the
B'...' syntax), it's plausible other typed-literal paths (CAST, UPDATE SET)
share the same coerceTextLikeDatum chokepoint and are now ALSO fixed as a
side effect — not verified this loop, low-priority spot-check if time
allows during bitmapops.sql work.

Gates run: go build ./... clean; targeted go test -run
'TestParseBitStringLiteral|TestParseBitStringLiteralInvalidDigit'
./internal/parser/ PASS; go test -run
'TestBitStringLiteralInsertRoundTrip|TestBitStringLiteralHexDecode'
./internal/executor/ PASS; RALPH_PRECOMMIT_SCOPE=units
ralph-precommit-test.sh PASS (full suite, ~460s dominated by
internal/initdb); make check-testport-inventory PASS; make regen-testport
ran clean; make ralph-state-guard PASS (self-repaired a stale
progress.json completed-marker, same recurring pattern as prior loops);
pre-commit hook's pgbench smoke PASS (337/618/12442 TPS across the 3
pgbench transaction types).

In-flight: none. (All throwaway test servers/datadirs from bit.sql
manual verification were stopped and rm -rf'd this loop — /tmp/goopg-bit*,
/tmp/bitdd*, tmp/regress-goopg-data all cleaned.)
