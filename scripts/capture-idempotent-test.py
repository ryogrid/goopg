#!/usr/bin/env python3
"""capture-idempotent-test.py — regression test for the M0137-0001 K18 fix.

Guards scripts/capture-tpch.sh and scripts/capture-tpcds.sh against the "$$"
tempfile trap: two consecutive captures of an UNCHANGED binary must diff
empty. Before the fix, the scratch SQL file was named after the capturing
process's PID (parity-capture-$$.sql); a query that fails to plan makes psql
prefix its error text with the `-f` filename it was given
(`psql:<path>:<line>: ERROR: ...`), so the PID leaked into the capture and
two runs of literally the same server produced a spurious diff (seen in
R122, R123, R124, R128).

This test stubs `psql` (no live server / cluster needed) so it runs in the
normal `go test`-adjacent gate, not as a manual bench-cluster procedure. The
stub answers a "good" query with a plan line and a "bad" query with a
psql-style `-f`-filename-prefixed ERROR line, exactly reproducing the shape
of the leak. It intercepts real psql via $PG_BIN, which both capture
scripts prepend onto PATH ahead of everything else — pointing $PG_BIN at
the stub directory is therefore sufficient; no PATH surgery needed.

Usage: python3 scripts/capture-idempotent-test.py [-v]
"""

import os
import stat
import subprocess
import sys
import tempfile
import unittest

ROOT = os.path.dirname(os.path.abspath(__file__)).rsplit(os.sep + "scripts", 1)[0]

STUB_PSQL = """#!/usr/bin/env bash
# Stub psql for capture-idempotent-test.py: ignores connection args, looks
# only at the file passed via -f. A query file containing BADQUERY reports a
# psql-style filename-prefixed ERROR (reproducing the K18 leak surface); any
# other query file reports a one-line fake plan.
file=""
prev=""
for arg in "$@"; do
    if [ "$prev" = "-f" ]; then file="$arg"; fi
    prev="$arg"
done
if [ -z "$file" ]; then
    echo "stub-psql: no -f given" >&2
    exit 1
fi
if grep -q BADQUERY "$file"; then
    echo "psql:${file}:1: ERROR: relation \\"badquery\\" does not exist"
    exit 1
fi
echo "SET"
echo " Seq Scan on lineitem  (cost=0.00..1.00 rows=1 width=1)"
exit 0
"""


def _write_executable(path, content):
    with open(path, "w") as fh:
        fh.write(content)
    st = os.stat(path)
    os.chmod(path, st.st_mode | stat.S_IEXEC | stat.S_IXGRP | stat.S_IXOTH)


class CaptureIdempotentTest(unittest.TestCase):
    def _stub_env(self, tmp):
        pg_bin = os.path.join(tmp, "pg_bin")
        os.makedirs(pg_bin)
        _write_executable(os.path.join(pg_bin, "psql"), STUB_PSQL)
        env = dict(os.environ)
        env["PG_BIN"] = pg_bin
        return env

    def _run_twice(self, script, extra_env, qdir_env):
        with tempfile.TemporaryDirectory() as tmp:
            env = self._stub_env(tmp)
            env.update(qdir_env(tmp))
            # Same $OUT path both times: this is the real-world scenario the
            # trap hit — re-running the identical capture command (same arm,
            # same output file) against an unchanged binary. Each run is a
            # separate process, so it always gets a fresh PID from the OS;
            # the fix makes the scratch filename depend on $OUT instead.
            out = os.path.join(tmp, "out.txt")
            texts = []
            for _ in range(2):
                proc = subprocess.run(
                    [os.path.join(ROOT, "scripts", script),
                     "5599", "db", "user", out, "capture-idempotent-test"],
                    env=env, capture_output=True, text=True)
                self.assertEqual(proc.returncode, 0,
                                  "%s failed: %s" % (script, proc.stderr))
                with open(out) as fh:
                    texts.append(fh.read())
            text1, text2 = texts
            # Sanity: the stub's error path actually fired, so this test
            # would have caught the pre-fix PID leak (the whole point of it).
            self.assertIn("ERROR: relation", text1)
            self.assertEqual(text1, text2,
                              "%s: two captures of an unchanged binary "
                              "diverged (K18 trap regressed)" % script)
            # M0137-0002: the capture is machine-stamped, not left resting on
            # the caller's hand-typed <header> alone (R122 §10's own words:
            # arm attribution "rests entirely on filename convention"). No
            # datadir was passed (5-arg call, matching every existing
            # caller), so the binary/PID field must degrade to an explicit
            # UNKNOWN rather than guess or silently omit itself.
            for field in ("# engine-id: ", "# repo-head: ", "# planner-flags: ",
                          "# pinned-GUCs: ", "# engine-binary: ", "# stats-epoch: "):
                self.assertIn(field, text1,
                              "%s: missing machine stamp field %r" % (script, field))
            self.assertIn("# engine-binary: UNKNOWN(no datadir given", text1)
            return text1

    def _run_with_datadir(self, script, extra_env, qdir_env):
        # Exercises the OTHER half of the M0137-0002 stamp: with a datadir
        # (a real postmaster.pid), the binary/PID fields must be populated,
        # not UNKNOWN — this is the "serving-PID /proc/<pid>/exe
        # verification" the milestone's Definition of Done names explicitly.
        with tempfile.TemporaryDirectory() as tmp:
            env = self._stub_env(tmp)
            env.update(qdir_env(tmp))
            datadir = os.path.join(tmp, "data")
            os.makedirs(datadir)
            with open(os.path.join(datadir, "postmaster.pid"), "w") as fh:
                fh.write("%d\n" % os.getpid())
            out = os.path.join(tmp, "out.txt")
            proc = subprocess.run(
                [os.path.join(ROOT, "scripts", script),
                 "5599", "db", "user", out, "capture-idempotent-test", datadir],
                env=env, capture_output=True, text=True)
            self.assertEqual(proc.returncode, 0,
                              "%s failed: %s" % (script, proc.stderr))
            with open(out) as fh:
                text = fh.read()
            self.assertIn("# engine-binary: pid=%d pid-alive=yes " % os.getpid(), text,
                           "%s: datadir given but binary/PID stamp not populated" % script)
            self.assertNotIn("# engine-binary: UNKNOWN", text)

    def test_capture_tpch_idempotent(self):
        def qdir_env(tmp):
            qdir = os.path.join(tmp, "tpch-queries")
            os.makedirs(qdir)
            with open(os.path.join(qdir, "Q1.sql"), "w") as fh:
                fh.write("select 1")
            # A query PG/goopg both fail to plan: exercises the ERROR leak
            # surface the K18 trap hid inside.
            with open(os.path.join(qdir, "Q2.sql"), "w") as fh:
                fh.write("select * from BADQUERY")
            q15a = os.path.join(tmp, "q15a.sql")
            with open(q15a, "w") as fh:
                fh.write("select 1")
            return {"TPCH_QUERY_DIR": qdir, "TPCH_Q15A_FILE": q15a}
        self._run_twice("capture-tpch.sh", {}, qdir_env)
        self._run_with_datadir("capture-tpch.sh", {}, qdir_env)

    def test_capture_tpcds_idempotent(self):
        def qdir_env(tmp):
            qdir = os.path.join(tmp, "tpcds-queries")
            os.makedirs(qdir)
            with open(os.path.join(qdir, "query1.sql"), "w") as fh:
                fh.write("select 1;")
            with open(os.path.join(qdir, "query2.sql"), "w") as fh:
                fh.write("select * from BADQUERY;")
            return {"TPCDS_QUERY_DIR": qdir}
        self._run_twice("capture-tpcds.sh", {}, qdir_env)
        self._run_with_datadir("capture-tpcds.sh", {}, qdir_env)


if __name__ == "__main__":
    unittest.main(verbosity=2)
