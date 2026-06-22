(idle — nothing in flight)

Loop #40: HARDENED the `FOR UPDATE/SHARE SKIP LOCKED` row-lock family to
pass-required (design 0118-0042). M0118-0003 is marked COMPLETE yet its
`skip-locked{,-2,-3,-4}` tests still ran through non-strict `runIsoSpec`
(silent t.Skip on a regression). All four already byte-match PG 18.3 (probe:
status=pass, empty diff, NO engine change), so flipped them to
`runIsoSpecStrict` + enriched the terse `-3`/`-4` docstrings.

Probe (zz_probe_test.go, deleted) over the M0118-0008 tail also reconfirmed
vacuum-skip-locked / vacuum-concurrent-drop / reindex-schema already pass strict
(already promoted in earlier loops). The genuinely-deferred specs all still
diverge and need real engine work:
  - plpgsql-toast: dollar-quoted-string ($$) parse error + advisory-gated
    VACUUM-blocks-TOAST behavior.
  - detach-partition-concurrently-{1,2,3,4}: needs DETACH PARTITION ...
    CONCURRENTLY parsing + the two-txn concurrent-detach protocol.
  - alter-table-{1,2,4}: ADD/VALIDATE CONSTRAINT lock semantics, INHERITS.
  - partition-concurrent-attach, partition-drop-index-locking,
    reindex-concurrently-toast, vacuum-no-cleanup-lock.

Files: internal/testport/isolation_port_test.go (4 helper switches +
docstrings); docs/design/0118-0042 + README index; port-status.csv D-002
rationale (comma-free, verified 70 lines / M0060-0004 tail intact) + regen
port-status.md; fix_plan M0118-0003 note.

Gates: TestPort_IsolationSkipLocked{,2,3,4} strict PASS; build+vet clean;
ralph-state-guard OK (repaired prev-loop completed marker); pgbench smoke =
pre-commit hook.

Next step: same-shape follow-up — promote the sibling `nowait*` family
(nowait{,-2,-3,-4,-5}, lock-nowait) + remaining M0118-0003 row-lock specs to
strict (probe-confirm byte-match, flip helper). Then tackle the M0118-0008
engine-work tail (cheapest first-divergence: plpgsql-toast dollar-quote parsing).
