#!/usr/bin/env python3
"""check-stats-epoch-test.py — regression test for scripts/check-stats-epoch.sh
(M0137-0006).

M0137-0002 stamped a `# stats-epoch: …` line into every capture-tpch.sh /
capture-tpcds.sh artefact, but left it a passive field nothing read back — the
deferral that file's task line records. R120 §6's standing rule ("re-take the
OFF baseline; all A/B numbers same-epoch") had no tool behind it. This test
guards the tool that closes that gap: two captures whose stats-epoch matches
must pass, a mismatch (or an UNKNOWN epoch on either side) must fail LOUDLY
(non-zero exit, both epoch values printed), and a file missing the stamp
entirely must fail as an operational error, not a silent skip.

Usage: python3 scripts/check-stats-epoch-test.py [-v]
"""

import os
import subprocess
import tempfile
import unittest

ROOT = os.path.dirname(os.path.abspath(__file__)).rsplit(os.sep + "scripts", 1)[0]
SCRIPT = os.path.join(ROOT, "scripts", "check-stats-epoch.sh")

EPOCH_A = "cd91584b25f11563"
EPOCH_B = "deadbeefcafebabe"
UNKNOWN = "UNKNOWN(query failed: dial tcp: connect: connection refused)"


def _write(tmp, name, lines):
    path = os.path.join(tmp, name)
    with open(path, "w") as fh:
        fh.write("\n".join(lines) + "\n")
    return path


def _run(*files):
    proc = subprocess.run([SCRIPT, *files], capture_output=True, text=True)
    return proc.returncode, proc.stdout, proc.stderr


class CheckStatsEpochTest(unittest.TestCase):
    def test_matching_epochs_pass(self):
        with tempfile.TemporaryDirectory() as tmp:
            off = _write(tmp, "off.txt", ["# header: OFF", "# stats-epoch: " + EPOCH_A, "plan"])
            on = _write(tmp, "on.txt", ["# header: ON", "# stats-epoch: " + EPOCH_A, "plan"])
            rc, out, err = _run(off, on)
            self.assertEqual(rc, 0, "matching epochs: expected exit 0, got %d (stderr=%s)" % (rc, err))
            self.assertIn(EPOCH_A, out)

    def test_mismatched_epochs_fail_loudly(self):
        with tempfile.TemporaryDirectory() as tmp:
            off = _write(tmp, "off.txt", ["# stats-epoch: " + EPOCH_A])
            on = _write(tmp, "on.txt", ["# stats-epoch: " + EPOCH_B])
            rc, out, err = _run(off, on)
            self.assertEqual(rc, 1, "mismatched epochs: expected exit 1, got %d" % rc)
            self.assertIn("MISMATCH", err)
            # Both epoch values must be printed — a mismatch that doesn't show
            # WHICH epochs differed would send the caller back to open both
            # files by hand, defeating the point of an automated check.
            self.assertIn(EPOCH_A, err)
            self.assertIn(EPOCH_B, err)
            self.assertIn(off, err)
            self.assertIn(on, err)

    def test_unknown_epoch_fails_not_silently_matches(self):
        with tempfile.TemporaryDirectory() as tmp:
            off = _write(tmp, "off.txt", ["# stats-epoch: " + EPOCH_A])
            broken = _write(tmp, "broken.txt", ["# stats-epoch: " + UNKNOWN])
            rc, out, err = _run(off, broken)
            self.assertEqual(rc, 1, "UNKNOWN epoch must fail, not pass-by-default")
            self.assertIn("cannot verify", err)

    def test_missing_stamp_is_operational_failure(self):
        with tempfile.TemporaryDirectory() as tmp:
            off = _write(tmp, "off.txt", ["# stats-epoch: " + EPOCH_A])
            unstamped = _write(tmp, "unstamped.txt", ["# header: no stamp here"])
            rc, out, err = _run(off, unstamped)
            self.assertEqual(rc, 2, "a file with no stats-epoch line at all is an "
                                     "operational failure (exit 2), not a mismatch (exit 1)")
            self.assertIn("no '# stats-epoch: ' line found", err)

    def test_three_way_match_and_mismatch(self):
        with tempfile.TemporaryDirectory() as tmp:
            a = _write(tmp, "a.txt", ["# stats-epoch: " + EPOCH_A])
            b = _write(tmp, "b.txt", ["# stats-epoch: " + EPOCH_A])
            c = _write(tmp, "c.txt", ["# stats-epoch: " + EPOCH_A])
            rc, _, _ = _run(a, b, c)
            self.assertEqual(rc, 0, "three matching epochs must pass")
            d = _write(tmp, "d.txt", ["# stats-epoch: " + EPOCH_B])
            rc, _, err = _run(a, b, d)
            self.assertEqual(rc, 1, "one odd-one-out among three must fail")
            self.assertIn(d, err)

    def test_missing_file_is_operational_failure(self):
        with tempfile.TemporaryDirectory() as tmp:
            off = _write(tmp, "off.txt", ["# stats-epoch: " + EPOCH_A])
            rc, out, err = _run(off, os.path.join(tmp, "does-not-exist.txt"))
            self.assertEqual(rc, 2)
            self.assertIn("no such file", err)

    def test_usage_requires_at_least_two_files(self):
        with tempfile.TemporaryDirectory() as tmp:
            off = _write(tmp, "off.txt", ["# stats-epoch: " + EPOCH_A])
            rc, out, err = _run(off)
            self.assertEqual(rc, 2)
            self.assertIn("usage:", err)

    def test_end_to_end_against_real_capture_stamp_output(self):
        # Not a fixture hand-written to fit the checker: this runs the REAL
        # scripts/capture-tpch.sh (M0137-0002's capture-stamp.sh) against a
        # stubbed psql and checks the checker against its actual output —
        # guards the two tools staying wired together, not just the checker
        # in isolation.
        import stat as statmod

        stub_psql = """#!/usr/bin/env bash
echo "SET"
echo " Seq Scan on lineitem  (cost=0.00..1.00 rows=1 width=1)"
exit 0
"""
        with tempfile.TemporaryDirectory() as tmp:
            pg_bin = os.path.join(tmp, "pg_bin")
            os.makedirs(pg_bin)
            psql_path = os.path.join(pg_bin, "psql")
            with open(psql_path, "w") as fh:
                fh.write(stub_psql)
            st = os.stat(psql_path)
            os.chmod(psql_path, st.st_mode | statmod.S_IEXEC | statmod.S_IXGRP | statmod.S_IXOTH)

            qdir = os.path.join(tmp, "tpch-queries")
            os.makedirs(qdir)
            with open(os.path.join(qdir, "Q1.sql"), "w") as fh:
                fh.write("select 1")
            q15a = os.path.join(tmp, "q15a.sql")
            with open(q15a, "w") as fh:
                fh.write("select 1")

            env = dict(os.environ)
            env["PG_BIN"] = pg_bin
            env["TPCH_QUERY_DIR"] = qdir
            env["TPCH_Q15A_FILE"] = q15a

            out1 = os.path.join(tmp, "out1.txt")
            out2 = os.path.join(tmp, "out2.txt")
            for out in (out1, out2):
                proc = subprocess.run(
                    [os.path.join(ROOT, "scripts", "capture-tpch.sh"),
                     "5599", "db", "user", out, "check-stats-epoch-e2e"],
                    env=env, capture_output=True, text=True)
                self.assertEqual(proc.returncode, 0, "capture-tpch.sh failed: %s" % proc.stderr)

            # The stub answers the stats-epoch probe's own psql -f call with
            # the SAME fixed, non-empty, non-error line every time (it
            # ignores the query text entirely), so BOTH captures compute the
            # same real epoch — the identical-server case, exercised
            # end-to-end through the actual capture-tpch.sh/capture-stamp.sh
            # pipeline instead of on a hand-written fixture.
            rc, out, err = _run(out1, out2)
            self.assertEqual(rc, 0, "identical stub server: expected MATCH, got rc=%d stderr=%s" % (rc, err))
            self.assertIn("MATCH", out)
            self.assertNotIn("UNKNOWN", out)


if __name__ == "__main__":
    unittest.main(verbosity=2)
