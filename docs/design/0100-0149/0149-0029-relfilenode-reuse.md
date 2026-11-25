# M0149\-0029 — Port recovery/t/032\_relfilenode\_reuse.pl

Status: skipped as a detour \(5 links on the TAP path, over the guard's 3\). Three links landed;
TestPort\_Recovery032RelfilenodeReuse is written and gated \(`GOOPG_TAP_032`\).

## Path\-to\-TAP

032 ← \(A\) PG nodes need `pg_prewarm` ← \(B\) goopg `pg_prewarm` ← \(C\) faithful `pg_relation_filenode` for every
`pg_class` row ← \(D\) M0149\-0029a ← \(E\) M0149\-0029b.

## Landed

- \(A\) The PG 18.3 test\-module toolchain. `scripts/build-pg183-toolchain.sh` extracts `postgres/` \(git archive of the
  submodule HEAD\) into `tmp/pg183-src` and configures it as `local_install` is \(`--enable-cassert
  --enable-tap-tests`\), plus `--enable-injection-points`. It installs into `tmp/pg183-inj` with `contrib/pg_prewarm`,
  `contrib/test_decoding` and `src/test/modules/injection_points`. `postgres/` and `postgres/local_install` stay
  untouched. In tapnode, `NewPGModules`, `InitFromBackupPGModules` and `ModulesBinDir` run PG nodes from it, and a
  missing toolchain fails the test with the build command.
- \(B\) `pg_prewarm(regclass, mode text, fork text, first_block int8, last_block int8) → int8`
  \(`internal/executor/operators_pg_prewarm.go`\). It follows contrib/pg\_prewarm.c's argument checks and messages:
  null relation, type and fork; invalid prewarm type; fork name; missing fork; block range. It pins and releases every
  block in range \('buffer'\) and returns the count. `CREATE EXTENSION pg_prewarm` installs version 1.2.
- \(C\) `relationStorageForOID` asks the seeded `pg_class` row first for system OIDs. goopg's catalog models system
  views and shared catalogs as ordinary tables of the current database, so `pg_relation_filenode` / `filepath` named
  `base/<db>/<oid>` files that do not exist. A view now has no storage \(NULL\), and a shared catalog maps to
  `global/<filenode>`. regress alter\_table 672 → 652 mismatched lines.

## Not done

- M0149\-0029a: `CREATE DATABASE … TEMPLATE` of a template with indexes \(goopg primaries\).
- M0149\-0029b: nonzero XLogRecord padding from a PG primary stops goopg standby replay.
- `pg_prewarm` 'read' reads through shared buffers, and 'prefetch' only counts blocks. There is no SELECT privilege
  check and no autoprewarm.

## M0149\-0029b — a standby must not write WAL at startup

The filed symptom ("padding bytes nonzero" on a PG primary's WAL) was not a decoding bug: PG zeroes the padding
\(InitXLogInsert: hdr\_scratch from MemoryContextAllocZero\). A byte comparison of segment 3 showed a 25\-byte
XLOG\_FPW\_CHANGE record of the standby's own at 0/3000028. goopg's startup logged XLOG\_FPW\_CHANGE whenever
full\_page\_writes differed from the buffer pool's flag, including on a standby. The walreceiver then trimmed the
primary's first 0x48 bytes of the segment as already present, splicing the primary's record after the standby's.
`logFPWChange` now applies UpdateFullPageWrites' condition: log only with wal\_level replica or logical, and only
outside recovery \(neither standby.signal nor recovery.signal\). 032's PG\-primary pairing now replays; it next fails on
M0149\-0029c \(pg\_prewarm finds a pg\_class row with no main\-fork file on the standby\).
