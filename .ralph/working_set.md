(idle — nothing in flight)

# Loop #75 result — template1 extension re-attribution ROOT-CAUSED (not subsumed)

Banner: nightly run id UNCHANGED (20260922-004850) → PgoutputInterop blocked.
Took the question the baton set: is M0119-0006's last bs item subsumed by the
`[!]` template1 namespace collision? **No — and correcting that is the
result.** No production change this loop.

## Reproduced, then measured the write and read sides separately
- Repro: `CREATE EXTENSION amcheck` in template1 → before restart
  template1=1 / postgres=0 (the per-database registry works at RUNTIME);
  after restart template1=0 / **postgres=1**.
- **Write side is NOT misrouted**: `base/1/3079` (template1's own heap) and
  `base/5/3079` both contain `amcheck`; the template0 control `base/4/3079`
  does not. So CREATE EXTENSION did write to template1's heap.
- **Read side is the bug**: `reloadUserExtensionsFromHeap` reads
  `base/<cat.DBOID()>` (= base/5) and attributes everything there to
  `"postgres"`, then iterates `ListDatabases()` — where bootstrap databases
  are skipped because they report `DatabaseOid` **0**
  (`internal/initdb/open.go:1573` says so outright). base/1 is never scanned.
- `base/5/3079` also holds a LIVE `amcheck` image, which is what makes this a
  re-attribution rather than a disappearance.

## Same CLASS as loop #74's fix, but NOT the same cause
bw missed databases because `ListDatabases()` was EMPTY when it ran; these
are present and skipped by an explicit `dbOid == 0` test. bw's fix is the
template, not the answer.

## The one open question before coding (why it is not fixed yet)
Why does `base/5/3079` carry a live `amcheck` image when the row's own scope
heap is `base/1`? The bs slice describes `deleteExtensionCatalogRow` as
stamping "the row's own scope heap + re-mirrors", so a deliberate mirror is
the likely source. Teaching the reload to scan base/1 WITHOUT resolving that
would register the extension TWICE — correctly under template1 and again
under postgres from the mirror — turning a wrong-attribution bug into a
duplicate-row bug. Establish what the mirror is for first.

## Gates
`go build ./...` implied by the unchanged tree; state guard OK; pgbench smoke
via the commit hook. No value gates — ZERO production diff (verified over
`internal/ cmd/`).

## Next loop
Check the nightly run id FIRST. M0119-0006's three bs items are now all
addressed (two fixed, this one root-caused and re-filed), so unless the
mirror question is taken up, move to the banner's next milestone: **M0122**
→ M0131 → M0134 → M0135/M0136 → M0095/M0110.

## Owner escalations OPEN — four
1. M0145-0018 cost-model no-go. 2. M0145-0001 lineage exhausted.
3. partition_aggregate's inventory row. 4. template1 namespace collision
(Option A vs B) — with the schema-scoping task depending on it.
