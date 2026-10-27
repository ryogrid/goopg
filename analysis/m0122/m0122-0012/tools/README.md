# M0122-0012 census tools

Committed copies (2026-10-27, owner) of the loop's gitignored `tmp/` helpers that M0122-0012 slices used to rank
and measure targets. Banner item 10's return-first rule (G1–G5) cites them. They assume the private clones the
slices used (`tmp/q17-clone` port 5591 for TPC-H, `tmp/q64-clone` port 5592 for TPC-DS SF0.25) and run every
server through `scripts/goopg-test-run.sh`. Release-PG reference numbers come from
`analysis/timing-after-planprity-match/pg-release-scripts/`.

| script | measures |
|---|---|
| `tpch-gap.sh` | TPC-H warm per-query timings (tpch-acceptance-runner, second pass) on a clone |
| `ds-instr.sh` | user-mode instructions per TPC-DS query (`perf stat -e instructions:u`) — the A/B of record |
| `ds-alloc.sh` | bytes / objects allocated per query |
| `ds-planinstr.sh` | planning vs execution split |
| `ds-warm.sh` | warm wall time per query |
| `rows-census.sh` / `rows-census.py` | goopg actual rows per plan node vs PG (algorithmic gaps) |
