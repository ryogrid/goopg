(idle — nothing in flight)

Last loop: M0119-0006 **18th slice landed** — the interval COLUMN gets PG's
native 16-byte `Interval` layout (`{time int64 @0, day int32 @8, month int32
@12}`, typlen 16 / typalign 'd'). It was stored as the literal TEXT the user
typed, so `ORDER BY i` put `2 hours` after `10 days`, `WHERE i > interval '10
days'` returned a different SET, `i = interval '30 days'` missed `1 mon`, and
the column echoed `2 hours` where PG prints `02:00:00`.

Finding worth carrying: goopg already had every interval mechanism
(`KindInterval`, `compareDatum`'s `interval_cmp_value` port, `formatInterval`,
`parser.ParseIntervalBody`) — all reachable from EXPRESSIONS, none from a
stored column. A missing routing arm, not a missing algorithm; that is why
every in-memory interval unit test passed while three answers were wrong. Look
for the same shape in the other types `pgindex_keydesc.go` names as heap-side
divergences: `numeric` (stores the decimal STRING, not base-10000 NumericData)
and `uuid` (36-char text under an attlen-16 descriptor) are still in that
class, and a real PG standby misreads both today.

`formatInterval` moved to leaf `internal/pgdatetime` — `internal/wal`'s
pgoutput is the SECOND decoder of the heap layout and cannot import the
executor.

Banner state (re-read this loop): M0130 fully checked, M-NIGHTLY has no open
items, so the banner falls through to M0119, then M0122.

Next loop: continue M0119-0006. Remaining: posting-list duplicate coverage in
the checkunique tier, `box`/`int4range` key encodings, the whole-database
(unscoped) pg_amcheck run — plus this loop's 3 new ledger rows (`interval(3)`
typmod at storage, `interval hour to minute` unparseable as a column type,
`interval[]` elements still text) and the standing ASCII-vs-Unicode
whitespace-trim divergence (one answer owed across all type input functions).

Gates run: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35); `TestPort_RegressSuite` PASS
(676 s — hard-won rule #5, mandatory after a codec change); pre-commit pgbench
smoke PASS. TPC-DS SF0.5 sweep NOT run (~1 h): no planner change, and the codec
change is scoped to a single new type arm — no TPC-DS query has an interval
column.

In-flight: none
