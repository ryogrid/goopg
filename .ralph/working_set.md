(idle — nothing in flight)

Last loop: M0119-0006 **19th slice landed** — the `uuid` COLUMN gets PG's
native 16-byte `pg_uuid_t` (typlen 16 / typalign 'c' / typstorage 'p'). It was
stored as the 36-char canonical TEXT behind a varlena header, i.e. 37 bytes
under a descriptor that says 16.

Finding worth carrying: unlike the interval slice, NO goopg answer was wrong —
`uuid_cmp` is a memcmp and lowercase-hex text compares in the same order, so
the divergence is invisible from inside the engine and visible only to a
reader that trusts the descriptor (a PG standby reads the first 16 text
characters as the uuid and finds every FOLLOWING column 21 bytes out of
position). Lesson: "all goopg tests green" is not evidence about heap-format
fidelity; check the published `pg_attribute` row against `encodeValuePG`'s
arm, type by type.

The units gate found the free unlock: `pgIndexKeyImageIsPGFaithful`'s guard
test was deliberately written to FAIL when the codec became faithful, so uuid
moved onto the PG-format index-tuple key path (`btree.PGCompareUUID`) in the
same loop. **`numeric` is now the LAST member of that heap-side-divergence
class** — it stores the decimal STRING, not base-10000 `NumericData`, and it
is the obvious successor slice: same five seams, plus a `PGCompareNumeric`
unlock and the M0130-S11.4 B2-a ledger row it would close.

Banner state (re-read this loop): M0130 fully checked, M-NIGHTLY's six
20260810-011258 items are all filed AND checked, so the banner falls through
to M0119, then M0122.

Next loop: continue M0119-0006. Candidates: the `numeric` heap flip (above,
highest value), posting-list duplicate coverage in the checkunique tier,
`box`/`int4range` key encodings, the whole-database (unscoped) pg_amcheck run,
plus this loop's 2 new ledger rows (`uuid[]` elements still text; no on-disk
migration for heap-format flips).

Gates run: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
(first run FAILED on the intended guard, then PASS after the unlock);
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35); `TestPort_RegressSuite` PASS
(161 s, `-timeout 40m` — the default 10 m panics the suite); pre-commit
pgbench smoke PASS. TPC-DS SF0.5 sweep NOT run (~1 h): no planner change, and
no TPC-DS query has a uuid column.

In-flight: none
