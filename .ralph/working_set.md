(idle — nothing in flight)

Last loop (#93): M0119-0006 28th slice — **a time-of-day with no seconds field
is ordinary PG input again** (`'2020-01-01 10:00'`). Committed + pushed. Design
`docs/design/0125-0007-pg-faithful-date-field-decode.md` §6 (+ README row edit),
1 ledger row.

M-NIGHTLY duty this loop: `ci/logs/action-items.md` is still nightly run
`20260811-014635` (12 items), all already filed under M-NIGHTLY (loop #87).
Nothing new to file; they stay PARKED per the banner.

What landed + what it FOUND: PG's `DecodeTime` requires hour+minute and reads
seconds only `if (*cp == ':')` (tm_sec = 0 otherwise), so `10:00` IS `10:00:00`.
goopg's two layout tables disagreed about that: `evalTypedStringLit` (expr.go)
lists `"2006-01-02 15:04"`, `parseCopyTimestamp` (copy_text.go) did not — and
the latter is what COPY TEXT, `encodeValuePG` and the array-element encoder
funnel through, so an INSERT of `'2020-01-01 10:00'` raised 22007 while the same
text as a typed literal parsed. Fixed once in `padTimeFields`
(`internal/pgdatetime/normalize.go`), covering `10:00+05`, `10:00 PM` and the
empty trailing field `10:00:`. Deliberately NOT rewritten (ledger + tests
pinning the refusal): `'10:00.5'`, which PG reads as `MM:SS.f` = `00:10:00.5`,
and `'10::00'`, an empty MINUTE field — a default there is a wrong time, not an
error. Also confirmed pre-existing and untouched: `parseCopyTimestamp` has no
layout for `'2020-01-01T10:00:00'` (T, no zone) or `'2020-01-01 10:00:00Z'`.

Banner state: M-NIGHTLY filing done; M0130 fully checked; banner falls through
to M0119 (M0119-0005 blocked on missing hash/gin/gist/spgist/brin AMs, so
M0119-0006 stays the actionable head), then M0122.

Next loop: per banner, M0119-0006 again. Candidates, cheapest first: the two
`T`/`Z` timestamp layouts just filed (`parseCopyTimestamp`); the `BC` era input
gap (needs a real `DecodeDateTime` field walk + era-aware output); the `numeric`
index-key display-scale divergence (`EncodeNumericKey` strips trailing mantissa
zeros — NOTE: the ledger's "trailing metadata" resume point is suspect, since
appending scale bytes to a memcmp'd blob key would break UNIQUE equality of
`1.0` vs `1.00`; re-derive before coding); array SLICES `a[1:2]` (lexer);
TOASTed / multi-dim / NULL-element arrays in logical decoding. Still blocked:
`box`/`int4range` key encodings and the unscoped whole-database pg_amcheck run.

Gates: build + vet clean; units (`RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh`) PASS; `TestPort_RegressSuite` PASS (252 s,
warm cache — needs `-timeout 45m`, the go default 10 m kills it);
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35); pgbench smoke via the commit
hook. New tests mutation-checked by reverting `normalize.go` (reproduces the
exact 22007s); expected values captured from a throwaway PG 18.3 cluster
(socket /tmp, port 5599 — stop it with `pg_ctl -D $(cat /tmp/pgoracle.path)
stop` if it is still up).

In-flight: none
