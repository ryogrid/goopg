(idle — nothing in flight)

## Loop #9 result — M0134-0177 landed

**Nightly triage:** `ci/logs/action-items.md` unchanged (run `20260828-235424`);
both `## AI-` items already have M-NIGHTLY rows (001 ticked, 002 open — Q5
timeout). Nothing new to file. Banner: M-NIGHTLY filing is unconditional but
SELECTION is subordinate to M0134 while any M0134 task is unchecked → worked
M0134 by ID ascending. `-0176a/-0176b` are letter sub-items, not the next ID.

**Task:** M0134-0177 — `test_setup.sql`. Case **PASSES** (237 lines, 100%
parity), CSV `not-tried` → `pass`/`pass_required=yes`. Design
`docs/design/m0134-0177-btree-split-posting-refill.md`.

**Five things worth carrying:**

1. **A "not-tried" case can be un-RUNNABLE, not just untested.** `test_setup.sql`
   already matched PG byte-for-byte; the runner just executed it twice (its own
   prerequisite, then the test). Before assuming a case diverges, check that the
   harness can run it *once*.

2. **The engine bug was found by the double-run, not by the case.** Second
   `COPY` into a populated `onek` → `panic="storage: not enough free space in
   page"` in `nbtree.mustInsertItemSorted`. Root cause: `pageItems` EXPANDS
   posting lists (one item per heap TID) and the split refill wrote each back as
   its own plain line pointer. Measured with a temporary `SPLITDBG` printf: an
   8132-byte leaf expanded to **21960** bytes; both halves of a 50/50 cut
   overflowed. Normal splits log ~8700 = "one page + the new item" — that ratio
   is the invariant to check if this area regresses.

3. **Revert-check with a full READBACK, not just "no panic".** The guard at HEAD
   fails `RangeScan returned 21396 entries, want 21600` — the old split path
   *silently dropped 204 index entries* on shapes that didn't crash. A no-panic
   assertion would have missed the worse half of the bug.

4. **Sibling-paths rule, size-model edition (3rd instance after
   `itemEncodedSize`/root-0040).** `byteAwareSplitLoc` priced items with a
   *different* expression than `itemEncodedSize` — no line pointer, no MAXALIGN,
   no postings. Fix funnels writer and budget through one function
   (`postingChunkLens`) and pins the equality in a test. Deleted the two
   superseded helpers rather than leaving them as wrong-model siblings.

5. **`pg-regress-runner.sh` quick-set A/B vs stashed HEAD is a cheap, sharp
   gate** for high-blast-radius storage changes: ~10 min a side, and
   "byte-identical, same 48 diff files, same line counts" is much stronger
   evidence than a bare pass count (the quick set sits at 4/52 for unrelated
   reasons, so the absolute number says nothing).

**Trap re-confirmed:** the throwaway-PG oracle is worth the 30s (`initdb -A
trust` + `pg_ctl -o "-p 5542 -k /tmp"`) — it settled that psql's
`psql:<file>:<line>:` NOTICE prefix is a `-f` artifact and that pg_regress uses
a **stdin redirect** (`pg_regress_main.c:75`), which is why expected files carry
bare `NOTICE:`. Separately: a `psql` against the TPC-H reference PG on **65432**
hung for 15 min — do not probe the bench clusters for one-off oracle questions.

**Gates run:** `go test ./internal/access/nbtree/... ./internal/storage/...`
PASS; `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12 rows=2 22.2s, Q13 rows=34 9.3s);
`scripts/pg-regress-runner.sh` quick set A/B **byte-identical**;
`scripts/pg-regress-runner.sh -v test_setup` PASS; double-`test_setup` repro 0
panics; `make regen-testport` + `make check-testport-inventory` PASS;
`make ralph-state-guard` OK (auto-repaired the progress marker). gofmt drift on
`btree.go` is **pre-existing at HEAD** (verified by stash); the new/changed
`posting.go`, `bulkload.go` and the new test file are gofmt-clean.

**NOT run:** TPC-DS SF0.5 gate (~1 h). This is an index-core change, so it is
the one gate worth adding if anything looks off downstream.

**In-flight:** none.
