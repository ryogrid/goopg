(idle — nothing in flight)

M0119-0006 57th slice landed: a `bpchar` value loses its declared width at
every render boundary. Committed and pushed.

Carry-forward #1 — **the same lesson as loop #69, now with a second data
point: a deferral resume point is a hypothesis.** The 56th slice's row said
`bpcharsend` IS `textsend`, so "the bytes are accidentally right", and that the
remaining decode padding needed a `copyBinaryToDatum` signature widening. Both
halves fell in ten minutes of measurement. `textsend` ships the STORED image,
and goopg stores `bpchar` trimmed where PG stores it padded — so the ENCODE
side, the half the row had cleared, was writing a 2-byte field where PG writes
10. And `copyBinaryToDatum` has taken a `catalog.Type` all along, whose `Args`
IS the typmod; `ParseCopyBinaryRows` passes `cols[i].Type`. Three older ledger
rows were repeating that same false blocker and are corrected in place.

Carry-forward #2 — **measure every boundary before scoping to one.** The row
framed this as a binary-COPY gap. Probing the same `char(10)` column across all
surfaces found the identical missing padding FOUR times: the `SELECT` DataRow,
`COPY … TO` text, CSV, binary, and the pgoutput change message. Scoping to COPY
would have left three-quarters of the defect and a fourth un-synced sibling.

Carry-forward #3 — **why it hid for so long is worth more than the fix.** The
two natural `psql` probes both conceal it: `length()` uses `bcTruelen`, and a
`||` operand goes through the rtrimming `bpchar`→`text` cast. A pre-existing
`dispatch.go` comment had drawn exactly that wrong conclusion in writing
("bpcharout uses bcTruelen which trims"); `bpcharout` is a bare
`TextDatumGetCString`. When a comment cites an upstream function as its
authority, read the function.

Carry-forward #4 — the multibyte probe was where the second bug fell out
(`coerceTextLikeDatum` measured the declared length in BYTES, so `'あい'` into
a `char(5)` was a spurious 22001). Any width-semantics slice should carry a
multibyte row.

Selection context for the next loop (re-verify, do not trust): banner now
names M0132 first after M-NIGHTLY (user directive 2026-08-13), ahead of M0131
and M0130's remaining items; M0133 is FILED-NOT-PROMOTED. M0132 is the
selectable milestone after M-NIGHTLY; M0119 remains the terminal drain
(M0119-0006 the largest open cluster).
The binary-`COPY` type chain is now EXHAUSTED — `bpchar` was its last named
type. Next candidates are the three rows this slice filed (`octet_length` off
the trimmed image; bare `bpchar` treated as `char(1)`; the trimmed heap image
itself) or the older 005 residuals (posting-list duplicates, box/int4range/
int4[] key types).

Gates: `go build ./...` clean; `internal/catalog` + `internal/executor` +
`internal/wal` + `internal/server` + `internal/pgnodes` PASS;
`TestPort_RegressSuite` PASS (271 s, `-timeout 40m` — the default 600 s timeout
KILLS it, see In-flight note below); `RALPH_PRECOMMIT_SCOPE=units` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35 canonical); TPC-DS SF0.5 sweep
PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0, plan shapes identical 99/99; pgbench
smoke PASS via the commit hook. Mutation-checked 3 ways (30 / 10 / 1 red).

Operational note for the next loop: `go test -run TestPort_RegressSuite
./internal/testport/` needs an explicit `-timeout 40m`. At the default 600 s it
panics with a goroutine dump that reads like a hang, not a timeout.

In-flight: none.
