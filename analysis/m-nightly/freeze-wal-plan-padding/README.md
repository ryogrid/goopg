# Freeze WAL plan padding (M-NIGHTLY, 2026-09-25)

goopg wrote `xlhp_freeze_plan` as 11 bytes; PG's struct is 12, with a pad
byte after `frzflags`. Before the fix, PG 18.3 `pg_waldump` described a
goopg freeze record of one plan and three offsets as:

    plans: [{ xmax: 0, infomask: 0, infomask2: 0, ntuples: 256, ...

After the fix (`0b37c784b`) it prints:

    plans: [{ xmax: 0, infomask: 0, infomask2: 0, ntuples: 3, offsets: [1, 3, 5] }]

Files:
- `xlog-tests.txt`: the freeze and waldump unit tests.
- `e2e-recovery.txt`: the PG-standby full cycle and three recovery ports.
- `sf025.txt`: the TPC-DS SF0.25 sweep.
