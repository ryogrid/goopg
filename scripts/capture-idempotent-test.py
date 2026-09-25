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
        # M12: a non-reference port now REQUIRES an explicit engine. These
        # tests exercise the K18/stamp path, not serving-binary verification
        # (CaptureServingBinaryVerifyTest covers that), so name the engine pg.
        env["CAPTURE_ENGINE"] = "pg"
        env.pop("GOOPG_EXPECT_BIN_SHA256", None)
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


class CaptureServingBinaryVerifyTest(unittest.TestCase):
    """H6 (METHODLOGY3 04-actions): a goopg capture must verify the serving
    binary before writing anything — datadir required, a "(deleted)" exe is
    refused, GOOPG_EXPECT_BIN_SHA256 is required and enforced, and a
    non-reference port without CAPTURE_ENGINE is refused (M12)."""

    PORT = "5599"

    def _env(self, tmp, **extra):
        pg_bin = os.path.join(tmp, "pg_bin")
        os.makedirs(pg_bin, exist_ok=True)
        _write_executable(os.path.join(pg_bin, "psql"), STUB_PSQL)
        qdir = os.path.join(tmp, "tpcds-queries")
        os.makedirs(qdir, exist_ok=True)
        with open(os.path.join(qdir, "query1.sql"), "w") as fh:
            fh.write("select 1;")
        env = dict(os.environ)
        env.pop("GOOPG_EXPECT_BIN_SHA256", None)
        env.update({"PG_BIN": pg_bin, "TPCDS_QUERY_DIR": qdir,
                    "CAPTURE_ENGINE": "goopg"})
        env.update(extra)
        return env

    def _datadir(self, tmp, pid):
        d = os.path.join(tmp, "data")
        os.makedirs(d, exist_ok=True)
        with open(os.path.join(d, "postmaster.pid"), "w") as fh:
            fh.write("%d\n%s\n1\n127.0.0.1:%s\n" % (pid, d, self.PORT))
        return d

    def _run(self, env, tmp, datadir=None):
        out = os.path.join(tmp, "out.txt")
        args = [os.path.join(ROOT, "scripts", "capture-tpcds.sh"),
                self.PORT, "db", "user", out, "h6-test"]
        if datadir:
            args.append(datadir)
        proc = subprocess.run(args, env=env, capture_output=True, text=True)
        return proc, out

    def test_goopg_requires_datadir(self):
        with tempfile.TemporaryDirectory() as tmp:
            proc, out = self._run(self._env(tmp), tmp)
            self.assertEqual(proc.returncode, 1, proc.stderr)
            self.assertIn("requires the server's datadir", proc.stderr)
            self.assertFalse(os.path.exists(out), "refused capture must not write $OUT")

    def test_goopg_live_binary_passes_and_sha_enforced(self):
        import hashlib
        with open("/proc/%d/exe" % os.getpid(), "rb") as fh:
            good = hashlib.sha256(fh.read()).hexdigest()
        with tempfile.TemporaryDirectory() as tmp:
            d = self._datadir(tmp, os.getpid())
            # M12: GOOPG_EXPECT_BIN_SHA256 is required for a goopg capture.
            proc, out = self._run(self._env(tmp), tmp, d)
            self.assertEqual(proc.returncode, 1, proc.stderr)
            self.assertIn("requires GOOPG_EXPECT_BIN_SHA256", proc.stderr)
            self.assertFalse(os.path.exists(out), "refused capture must not write $OUT")
            proc, _ = self._run(self._env(tmp, GOOPG_EXPECT_BIN_SHA256=good), tmp, d)
            self.assertEqual(proc.returncode, 0, proc.stderr)
            proc, _ = self._run(self._env(tmp, GOOPG_EXPECT_BIN_SHA256="0" * 64), tmp, d)
            self.assertEqual(proc.returncode, 1, proc.stderr)
            self.assertIn("GOOPG_EXPECT_BIN_SHA256", proc.stderr)

    def test_goopg_port_mismatch_refused(self):
        with tempfile.TemporaryDirectory() as tmp:
            d = self._datadir(tmp, os.getpid())
            env = self._env(tmp)
            out = os.path.join(tmp, "out.txt")
            proc = subprocess.run(
                [os.path.join(ROOT, "scripts", "capture-tpcds.sh"),
                 "5598", "db", "user", out, "h6-test", d],
                env=env, capture_output=True, text=True)
            self.assertEqual(proc.returncode, 1, proc.stderr)
            self.assertIn("does not name port", proc.stderr)

    def test_goopg_deleted_exe_refused(self):
        import shutil
        with tempfile.TemporaryDirectory() as tmp:
            binpath = os.path.join(tmp, "fake-goopg")
            shutil.copy2(shutil.which("sleep"), binpath)
            child = subprocess.Popen([binpath, "30"])
            try:
                os.unlink(binpath)
                d = self._datadir(tmp, child.pid)
                proc, _ = self._run(self._env(tmp), tmp, d)
                self.assertEqual(proc.returncode, 1, proc.stderr)
                self.assertIn("(deleted)", proc.stderr)
            finally:
                child.kill()
                child.wait()

    def test_unknown_engine_on_private_port_refused(self):
        with tempfile.TemporaryDirectory() as tmp:
            env = self._env(tmp)
            env.pop("CAPTURE_ENGINE", None)
            d = self._datadir(tmp, os.getpid())
            proc, out = self._run(env, tmp, d)
            self.assertEqual(proc.returncode, 1, proc.stderr)
            self.assertIn("set CAPTURE_ENGINE=goopg|pg explicitly", proc.stderr)
            self.assertFalse(os.path.exists(out), "refused capture must not write $OUT")

    def test_pg_engine_not_checked(self):
        with tempfile.TemporaryDirectory() as tmp:
            proc, _ = self._run(self._env(tmp, CAPTURE_ENGINE="pg"), tmp)
            self.assertEqual(proc.returncode, 0, proc.stderr)


if __name__ == "__main__":
    unittest.main(verbosity=2)
