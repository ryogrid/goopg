(idle — nothing in flight)

Loop #41: COMPLETED the M0118-0003 row-lock hardening begun in 0118-0042.
Promoted the remaining 16 dedicated row-lock isolation tests from soft
`runIsoSpec` (silent t.Skip on a regression) to strict `runIsoSpecStrict`
(hard red): nowait{,-2,-3,-4,-5}, lock-nowait, tuplelock-{conflict,update,
partition,upgrade-no-deadlock}, lock-update-{traversal,delete},
update-locked-tuple, propagate-lock-delete, lock-committed-{update,keyupdate}.
All 16 already byte-match PG 18.3 — NO engine change. **All 20 M0118-0003
row-lock specs are now strict; none can regress silently.**

Verified: single `go test -run` over the family through scripts/goopg-test-run.sh
returned `ok` in ~83 s (strict helper fails, never skips, on a non-pass — so
`ok` proves every one passed). go vet clean.

Files: internal/testport/isolation_port_test.go (16 helper switches);
docs/design/0118-0043-rowlock-family-strict.md + README index row;
fix_plan M0118-0003 note.

Gates: 16 promoted tests strict PASS; build+vet clean; ralph-state-guard OK
(repaired prev-loop completed marker); pgbench smoke = pre-commit hook.

Next step: tackle the M0118-0008 engine-work tail (cheapest first-divergence
first: plpgsql-toast dollar-quoted-string ($$) parsing, then
detach-partition-concurrently / alter-table-{1,2,4} ADD/VALIDATE CONSTRAINT
lock semantics). These need real engine work, not a helper flip.
