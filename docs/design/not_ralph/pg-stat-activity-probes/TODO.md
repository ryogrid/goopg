# TODO — pg_stat_activity probes (branch `waitevent-impl`)

## Phase A — design docs
- [x] A1 Survey upstream probes (`01`), audit goopg current state (`02`)
- [x] A2 Write design bundle: README, 01–04, TODO
- [ ] A3 Sub-agent review pass; resolve findings
- [ ] A4 Commit (`-n`) + push branch

## Phase B — implementation
- [ ] B1 Intern any missing wait-event names in `internal/utils/activity`
- [ ] B2 `Manager.WaitForXID`: defer-balanced `Lock:transactionid` window
      (+ optional registry/procNum registration setter, nil = disabled)
- [ ] B3 `evalPgSleep`: `Timeout:PgSleep`
- [ ] B4 Advisory wait path: wire `Lock:advisory` around the `ready chan`
      park (advisory.go:47/:220 — real park confirmed by review)
- [ ] B5 Unit tests: zero-alloc (AllocsPerRun), WaitForXID snapshot parity,
      PgSleep event, advisory test
- [ ] B6 Gates: `go build ./...`, targeted `go test`, `go vet ./...`;
      commit (`-n`) + push

## Phase C — live verification
- [ ] C1 Init + start throwaway cluster :5533 via cgroup cap; pgbench -i -s 10
- [ ] C2 Write `scripts/pgbench-wait-sample.sh` (500 ms sampling of client
      backends, CSV, end-of-run aggregation by wait event, pprof capture)
- [ ] C3 Run `pgbench -N -c 50 -j 8 -T 60 -s 10`; check distribution vs §5
      pass criteria in 03-design.md
- [ ] C4 Contention mode run (small scale) showing `Lock:transactionid` rows
- [ ] C5 pprof `-top` correlation; confirm probe sites ≈0% of CPU samples

## Phase D — wrap-up
- [ ] D1 Update this TODO; final summary with observed distribution table
- [ ] D2 Final commit + push
- [ ] D3 Post-merge compensation gates from main tree:
      `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` +
      `scripts/tpch-spotcheck.sh`
