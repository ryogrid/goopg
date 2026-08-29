(idle — nothing in flight)

## Loop #7 result — M0134-0175a landed

**Nightly triage:** `ci/logs/action-items.md` unchanged (run `20260828-235424`);
both `## AI-` items already have M-NIGHTLY rows. Nothing new to file.

**Baton check:** previous baton said `(idle)`; tree had zero modified `.go` files.

**Task:** M0134-0175a — `fillfactor` was never applied when choosing a heap
insert page. `tablesample.sql` **304 → 214** diff lines; the first 63 lines of
the case (every sampled `SELECT`) now match the oracle byte-for-byte. Design
`docs/design/m0134-0175a-fillfactor-at-insert.md`.

**Four things worth carrying:**

1. **The *declared but unconsumed* pattern, third instance.** `fillfactor` was
   parsed, bounds-checked, persisted to `pg_class.reloptions`, pg_dump'd,
   ALTER-able and read by the **cost model** — with no consumer in the insert
   path. Everything around a feature can exist while the feature does nothing.
   Grep for a *consuming* reference, not any reference.

2. **The default has to be arithmetically inert, and that is the test.** PG's
   `fillfactor=100` gives `saveFreeSpace=0` and `targetFreeSpace=MAXALIGN(len)`
   — exactly the pre-existing "does it physically fit" check. The control test
   `TestDefaultFillfactorPacksTightly` (ten rows, no reloption, still ONE block)
   is what makes the "TPC-H/TPC-DS density is untouched" claim checkable rather
   than asserted. Two other upstream properties are load-bearing: a freshly
   extended page is exempt from the reserve (hio.c:859), and the
   `nearlyEmptyFreeSpace` clamp stops a 4 KiB tuple in a `fillfactor=10` table
   from demanding ~11 KiB on an 8 KiB page and extending forever
   (revert-checked; both revert-checks bite).

3. **`catalog.InMemory` has NO OID index.** `LookupTableByOIDAllDBs` walks every
   namespace's `map[string]*Table`, so any per-row catalog resolution on a write
   path is an O(tables) scan under a shared RLock — the M0107 shape. The
   workaround here is a per-session memo (`Context.heapFillfactorCache`,
   following the `pgKeyDescCache` precedent) invalidated by ALTER. **If a future
   task needs cheap rel→table on a hot path, add the OID index first**; the
   ledger row names it as the real fix.

4. **The sibling was checked and deliberately NOT changed.**
   `writeHeapRowReturningPG` has a near-identical block-selection loop but its
   only caller is `writeHeapRowCanonical` (system catalogs ⇒ no reloptions ⇒
   fillfactor always 100), so the gate there would be unreachable. The reason
   and the port instruction are in a comment at the function. "Sibling paths
   must change together **or say why they didn't**."

**Discovered, filed, not fixed: M0134-0175e** — a second `FETCH FIRST` on an
already-scrolled SCROLL CURSOR returns 0 rows (PG restarts and returns 3,4,5…).
The first pass is byte-correct, so it is a portal rewind gap, not sampling; it
was masked while all ten rows sat on one block. Compare `PortalRunFetch` /
`DoPortalRewind` (`pquery.c:1400`).

**Gates run:** `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
PASS (no FAIL lines; `internal/initdb` 462s cold, rest cached);
`scripts/tpch-spotcheck.sh` PASS (Q12 rows=2 20.9s, Q13 rows=34 8.3s);
`scripts/pg-regress-runner.sh tablesample` 214 lines;
`make check-testport-inventory` PASS; `make ralph-state-guard` OK (auto-repaired
the progress marker). `gofmt -l` flags operators_storage/context/operators_ddl
**at HEAD too** — the known go1.25-baseline vs local-go1.26 mismatch, not this
change; the two new files are clean.

**In-flight: none.**

**Carried obligations (21st loop):** TPC-DS SF0.5 gate still NOT run (for -0156,
-0157). -0158..-0175a are parser/DDL/catalog/ACL/wire/type-input/FK/plpgsql/
pubsub/sampling/heap-page-density only; -0175a moves on-disk density **only for
tables that set `fillfactor`**, and no TPC-DS table does, so it still cannot
move a TPC-DS plan. The abandoned 4-case regress A/B from loop #6 remains
unresumed (warm worktree `tmp/ab-head` no longer verified to exist).
