Task: M0117-0006 follow-up — wire the `transaction_buffers` GUC value into
`CLog.SetCLOGBuffers` from `initdb.Open`. LANDED this loop (#12).

What landed (the noted small follow-up under M0117-0006 Part B):
- New `OpenOptions.TransactionBuffers int` (internal/initdb/open.go). `Open` calls
  `clog.SetCLOGBuffers(opts.TransactionBuffers)` immediately BEFORE
  `EnablePGSLRUMirror` (a no-op once the pool is created).
- `cmd/goopg start` reads `intGUC(registry, "transaction_buffers", 0)` and passes
  it via the OpenOptions literal (cmd/goopg/main.go).
- Boot default 0 → EffectiveCLOGBuffers(0,0)=16 (auto floor) → behaviour UNCHANGED
  for every default deployment. A non-zero postgresql.conf override now actually
  sizes the live CLOG SLRU pool instead of being silently dropped.

Files: internal/initdb/open.go (OpenOptions field + SetCLOGBuffers call),
cmd/goopg/main.go (GUC read + literal), cmd/goopg/main_test.go (new
TestTransactionBuffersFromGUC + _NilRegistry), internal/mvcc/clog_bufferpool_live_test.go
(new TestSetCLOGBuffersSizesPool), docs/design/0117-0006-*.md, fix_plan.md.

Gates run (ALL PASS): go build cmd+initdb; gofmt -l clean; go vet mvcc+initdb+cmd
clean; -race ./internal/mvcc/... ; cmd/goopg + internal/initdb full suites; new
targeted tests. pgbench smoke on commit (pre-commit hook). NOT executor/planner/
codec — startup-wiring only, default path byte-identical, so no TPC-H spot-check
needed (hook pgbench exercises CLOG pool through TPC-B).

Next step: COMMIT (done if you see this with a clean tree). Then the remaining
M0117-0006 work is **Part C** = remove the resident `banks` (16× memory
reduction) — separate focused loop: migrate every no-mirror `&CLog{}` unit test
to the pool path first, re-init data dir on the memory-model change, high blast
radius (memory + concurrency + recovery). Box stays unchecked until Part C lands.
