(idle — nothing in flight)

M0119-0006 34th slice landed: the numeric index key has no display scale, and
cannot be given one. Committed and pushed.

The finding worth carrying: **a deferral ledger's resume point is a hypothesis,
not an instruction.** This row's said "carry the display scale in the key — it is
trailing metadata, so it need not disturb the order-preserving mantissa run".
Order was never the binding constraint. EQUALITY was: `EncodeNumericKey` strips
trailing mantissa zeros so `1.0` and `1.00` encode to the same bytes, and that
byte identity is the entire mechanism by which `UNIQUE` on `numeric` raises
23505 — which is what PG does, since `numeric_cmp` ignores display scale.
Byte-identical keys cannot also distinguish two spellings of one number. Ten
minutes of probing (a throwaway `zz_probe_test.go` printing the two encodings
plus a live duplicate INSERT) refuted the resume point before any code was
written; the ledger row now says so and its successor row names the real seam.

Second point, the shape of the fix: the scan was asking ONE question where there
are two — "can these bytes be inverted to the right VALUE" (what
`bt_index_check`'s comparator needs, and `numeric` answers yes) versus "does the
resulting Datum SPELL the value the way the heap spells it" (what an index-only
scan needs, and `numeric` answers no). Conflating them is what made the
containment look expensive in the 27th slice: refusing `numeric` in the DECODER
would have disabled `bt_index_check` on every numeric index, for a loss that buys
nothing. Splitting the predicate costs one function.

Third: the same defect had two code paths with different exposure. The scalar
`numeric` arm did NOT reproduce, because that index takes the PG tuple-image key
path, which carries per-attribute datums and loses no spelling. So the new
refusal is asked only of the blob key format — and the E2E test carries the
scalar arm anyway, since which format an index gets is not a property the test
should depend on.

Selection context for the next loop (re-verify, do not trust): M-NIGHTLY had zero
open items at this loop's triage (all 17 `AI-20260813-*` filed and closed);
M0131's two unchecked items (S9, S24) are both deferred-with-ledger-row —
S9 only because S9.4 became M0133; M0130 has zero unchecked items. That leaves
M0119 as the selectable drain, where M0119-0006 is the largest open cluster and
takes one slice per loop off its residual list.

Gates: `go build ./...` clean; `go vet ./internal/executor/` clean;
`internal/executor` + `internal/access/btree` PASS; new
`TestNumericIndexOnlyScanKeepsDisplayScale` PASS and proven fail-when-broken
(array arm reports `{2.7}` want `{2.70}` with the check removed);
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35 canonical); TPC-DS SF0.5 sweep
PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0, plan shapes identical 99/99; UNITS PASS;
pgbench smoke PASS via the commit hook.

In-flight: none.
