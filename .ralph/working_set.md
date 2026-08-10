(idle — nothing in flight)

Last loop (#94): M0119-0006 29th slice — **the ISO 8601 `T` separator and the
`Z` zone are ordinary PG timestamp input again**. Committed + pushed. Design
`docs/design/0125-0007-pg-faithful-date-field-decode.md` §7 (+ README row edit),
1 ledger row.

M-NIGHTLY duty this loop: `ci/logs/action-items.md` is still nightly run
`20260811-014635` (12 items), all already filed under M-NIGHTLY (loop #87).
Nothing new to file; they stay PARKED per the banner.

What landed + what it FOUND: `'2020-01-01T10:00:00'` — plain ISO 8601, what
every JSON encoder and `date -Is` emits — raised 22007. So did `…t10:00:00`,
`2020-01-01 10:00:00Z`, `…z`, `… Z`, every `T`-separated offset form, and on
BOTH separators any offset wider than two digits (`+0530`, `+05:30`). Root
cause: Go's `RFC3339`/`RFC3339Nano` constants demand the `T` AND a zone, so a
zone-less `T` form matched neither them nor the space-separated layouts. Fixed
structurally, since this was the THIRD consecutive slice to catch goopg's two
timestamp layout tables disagreeing: ONE shared `pgTimestampLayouts`
(`copy_text.go`, separator × offset-width via Go's `Z07*` elements, zone-bearing
first) now iterated by `parseCopyTimestamp` AND `evalTypedStringLit`, with
case/spacing folded upstream in `pgdatetime.NormalizeInput` (separator scan
breaks on `t`; new `canonicalZulu`). `canonicalZulu` requires a DIGIT before the
letter so it cannot touch zone abbreviations — folding `'10:00:00 NZ'` to UTC
would be a silent 12-hour error. Deferred (ledger + refusal tests): a zone on a
bare DATE (`'2020-01-01Z'`, PG accepts) and abbreviation lookup generally
(needs a `datetbl` port).

Banner state: M-NIGHTLY filing done; M0130 fully checked; banner falls through
to M0119 (M0119-0005 blocked on missing hash/gin/gist/spgist/brin AMs, so
M0119-0006 stays the actionable head), then M0122.

Next loop: per banner, M0119-0006 again. Candidates, cheapest first: the `BC`
era input gap (needs a real `DecodeDateTime` field walk + era-aware output —
note the `'10:00.5'`/`'10::00'`/`'2020-01-01Z'` rows all resolve into that same
field walk, so it is now a 4-in-1); the `numeric` index-key display-scale
divergence (`EncodeNumericKey` strips trailing mantissa zeros — the ledger's
"trailing metadata" resume point is SUSPECT, since appending scale bytes to a
memcmp'd blob key would break UNIQUE equality of `1.0` vs `1.00`; re-derive
before coding); array SLICES `a[1:2]` (lexer); TOASTed / multi-dim / NULL-element
arrays in logical decoding. Still blocked: `box`/`int4range` key encodings and
the unscoped whole-database pg_amcheck run.

Gates: build + vet clean; units (`RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh`) PASS; `TestPort_RegressSuite` PASS (249 s,
warm cache — needs `-timeout 45m`, the go default 10 m kills it);
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35); pgbench smoke via the commit
hook. Both halves of the fix mutation-checked independently. Expected values
captured from a throwaway PG 18.3 cluster (socket /tmp, port 5599, datadir
/tmp/pgoracle-loop94 — stop it with `pg_ctl -D $(cat /tmp/pgoracle.path) stop`
if it is still up).

In-flight: none
