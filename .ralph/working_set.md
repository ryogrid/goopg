(idle — nothing in flight)

Last loop: **M0127-P5.9-d CLOSED** — clause 1's instrument exists and is
calibrated. Committed + pushed. Facts the next loop must NOT re-derive:

1. `cmd/tpch-runner -digest` emits `colsig` / `ordered` / `unordered` per result
   set; `-diff A.log B.log` compares two arms and exits 1 on any non-MATCH.
   `NO-DIGEST` and `BOTH-ERROR` are FAILING verdicts by design.
2. **`rows=N` must stay the LAST token on the OK line.** Three gate scripts
   (`tpch-spotcheck.sh:249`, `stage-tpch.sh:193`, `tpch-relsize-arm.sh:323`)
   extract it with an end-of-line-anchored regex. Digests go BEFORE it.
   `TestOKLineKeepsRowsTerminal` pins this with the gates' own regex — do not
   "tidy" the token order.
3. Calibration DONE: flag-OFF arm vs itself = **24/24 MATCH**, four server
   processes, two engine images. Tie-prone Q3/Q10/Q16/Q15a matched on the
   ORDERED digest too → a clean run yields no spurious `ORDER-DIFF`. `-digest`
   costs ~2 % (389 s vs 380.1 s). Evidence:
   `analysis/leftdeep-joins/2026-08-05-p59d-digest-selfdiff.txt`.
4. `scripts/tpch-acceptance-arm.sh` is now IN THE REPO (promoted from tmp/).
   Use it for both arms of the re-run; it refuses a busy port and a live
   nightly, and builds to `tmp/goopg-acceptance-bin` (never the shared
   `tmp/goopg-bench-bin`). Pass `NO_BUILD=1` on the second arm to hold ONE
   binary across both — that is what makes the arms comparable.
5. Digest design decisions that are load-bearing (all test-pinned): SUM not XOR
   for the multiset digest (XOR cancels a duplicated row); length-prefixed
   fields not delimiters (a text column can contain any delimiter); an explicit
   NULL marker byte (else NULL hashes as `''`).
6. Ledgered blind spot: the diff compares two GOOPG arms, never PG. A value
   wrong in both arms reports MATCH. Oracle arm + rendering canonicalisation is
   the resume point in the ledger row.

Files: `cmd/tpch-runner/{digest.go,digestdiff.go,main.go,README.md}` + two new
test files; `scripts/tpch-acceptance-arm.sh` (new).
Docs: 09 §3.3 (new) + §3 clause 1 amended, leftdeep-joins/README.md,
IMPLEMENTATION-TODO P5.9-d, fix_plan P5.9-d + P5.9 parent, 1 ledger row.

Gates run: UNITS (`RALPH_PRECOMMIT_SCOPE=units`) PASS incl. cmd/tpch-runner;
SPOT (`scripts/tpch-spotcheck.sh`, private GOOPG_BIN) PASS — Q12 rows=2,
Q13 rows=35, 28.2 s — which is also the proof the changed output format still
parses in the real gate; the SF1 self-diff above; `make ralph-state-guard`
(repaired the stale progress marker, then OK); pgbench smoke via the commit hook.

Nightly triage 20260805-014309: unchanged, both items already filed under
M-NIGHTLY, left unchecked per the banner.

Next step: **M0127-P5.9-e** — Q17 at flag ON, re-measured on top of the P5.9-c
fix. Run it ALONE on a fresh server (`QUERIES=17 PGSHAPED=1
scripts/tpch-acceptance-arm.sh q17on /tmp/q17-on.txt`) against the same
flag-OFF arm. Bar: ≤ 2× its flag-OFF 20.93 s, or an attributed finding — if it
still hangs, PROFILE it (low CPU + large heap ⇒ allocation, not a spin) rather
than re-reading EXPLAIN. P5.9 (the full bar re-run + flip) comes after -e.

In-flight: none.
