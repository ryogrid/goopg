(idle — nothing in flight)

M-NIGHTLY `AI-20260811-014635-002` (+ `-20260812-005501-004`,
`-20260813-005117-017`, `TestPort_IsolationReceiptReport`) fixed, committed and
pushed. `TestPort_IsolationMultipleCic` closed as STALE in the same loop
(verified NOT attributable to this fix — it passes with the fix stashed out).

**The finding worth carrying: three nightly items were carried for two days
under the wrong subsystem because the test's SUBJECT implied one.**
`receipt-report.spec` is serializable read-only-deferrable, so the baton and the
fix_plan both carried it as "a genuine SSI failure, likely the richest". It was
never SSI. It died in *global setup* on a system-catalog btree split. Re-running
the repro before theorising (selection rule §1) is what surfaced that in one
command — the failure text named `pg_index_indrelid_index`, nothing SSI.

Root cause worth generalising: goopg maintains bootstrapped system btrees in two
halves that never reference each other. `insertCanonicalSysBtreeLeaf` appends to
the leaf-root and **never consults** `keyMetaForSysBtree`; only the split and
multi-level descent read that registry, and only once a leaf-root has filled. So
an unregistered index is perfect for its first page of entries and then fails
forever. That is a **latency-shaped** sibling-path divergence: the two halves
disagree at write time but the disagreement is invisible until a size threshold
is crossed — `receipt-report` reached it only at permutation 152, which is
exactly why it read as flakiness. Nine indexes had accumulated in that state
across M0130/M0131 slices; the spec only ever named one. Registering all nine
was the point, not fixing 2678.

The guard had to be a **source pin** for a reason worth reusing: the defect lives
in the relationship between two call graphs, so any assertion written against
either half alone is satisfied by the broken state. Non-vacuity matters doubly
for source scanners — it carries a resolved-call-site floor (59, fails under 50)
and fails rather than skips on a non-literal OID argument, so a renamed helper
cannot turn it into a silent pass. Verified fail-when-broken.

Ledger row filed: only 2678 is exercised through a real split; the other eight
layouts are correct-by-construction from their builders but untested at split
time — 2693 (oid+name {80,2}) and 3081 (name {72,1}) are the two worth
distrusting.

Selection context for the next loop: M0131's only two unchecked items (S9, S24)
are both deferred-with-ledger-row — re-verified this loop against the ledger,
not taken from the baton — so M-NIGHTLY stays selectable per the banner.
Remaining open M-NIGHTLY items: `TestE2E_FailoverPGtoGoopg` subtest `async`;
the 11 regress normalization cases (re-run the repro FIRST — a full
`TestPort_RegressSuite` ran GREEN two commits after that nightly's sha, so these
may all be stale).

Gates: `go build ./...` + `go vet` clean; `go test ./internal/executor/` PASS
(6.1 s); target spec FAIL→PASS (6.8 s); new guard PASS + proven fail-when-broken;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35 canonical); pgbench smoke PASS
via the commit hook.

In-flight: none.
