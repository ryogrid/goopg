#!/usr/bin/env python3
"""estimate-parity-gate-test.py -- tests for the M0146-0009d vacuous-PASS
hardening of scripts/estimate-parity-gate.sh + scripts/estimate-parity/parity.py.

The defect: pg_isready on EA_PORT only proves *something* speaks the wire
protocol. On 2026-09-28 a foreign postgres squatted on :5534, short-circuited
start_server, answered all 99 queries with `relation does not exist`, and the
gate printed `PASS (52 fixed)` — a repin then wrote a 0-entry baseline over
the pinned one. These tests pin the three refusal points added for it:

  1. verify_server / EA_VERIFY_ONLY — the port's server must be this gate's
     own goopg serving EA_DATA (pidfile + live pid + cmdline + listen addr +
     wire version()), not just any protocol responder.
  2. The capture must contain query sections that are not all ERRORs.
  3. parity.py must exit non-zero — before any baseline write or ratchet
     verdict — when the capture scores zero nodes.

Usage: python3 scripts/estimate-parity-gate-test.py [-v]
"""

import os
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(HERE)
GATE = os.path.join(HERE, "estimate-parity-gate.sh")
PARITY = os.path.join(HERE, "estimate-parity", "parity.py")

VALID_CAPTURE = """\
===== Q1 =====
                          QUERY PLAN
------------------------------------------------------------------
 Seq Scan on store_sales  (cost=0.00..100.00 rows=100 width=8) (actual rows=100 loops=1)
 Planning Time: 0.1 ms
 Execution Time: 0.2 ms

===== Q2 =====
                          QUERY PLAN
------------------------------------------------------------------
 Seq Scan on item  (cost=0.00..50.00 rows=50 width=8) (actual rows=50 loops=1)
 Planning Time: 0.1 ms
 Execution Time: 0.1 ms
"""

# psql renders file-mode errors mid-line (`psql:<file>:<line>: ERROR:  …`),
# which is why the matchers must not anchor `ERROR:` at column 0.
ALL_ERROR_CAPTURE = """\
===== Q1 =====
                          QUERY PLAN
------------------------------------------------------------------
psql:ea-q.sql:3: ERROR:  relation "store_sales" does not exist
LINE 1: select count(*) from store_sales;

===== Q2 =====
                          QUERY PLAN
------------------------------------------------------------------
psql:ea-q.sql:3: ERROR:  relation "item" does not exist
"""

NO_SECTIONS_CAPTURE = """\
# a capture header with no ===== Qn ===== blocks at all
"""


def run_parity(capture_path, pgdir, extra=None):
    cmd = ["python3", PARITY, capture_path, pgdir]
    if extra:
        cmd += extra
    return subprocess.run(cmd, capture_output=True, text=True)


def run_gate(env_extra):
    env = dict(os.environ)
    env.update(env_extra)
    env.setdefault("EA_VERIFY_ONLY", "1")
    return subprocess.run(["bash", GATE], capture_output=True, text=True,
                          env=env, cwd=REPO)


def write(tmp, name, text):
    p = os.path.join(tmp, name)
    with open(p, "w", encoding="utf-8") as fh:
        fh.write(text)
    return p


def write_pidfile(datadir, pid, port, dd=None):
    """goopg postmaster.pid format: pid, datadir, started_ms, listen, sock."""
    with open(os.path.join(datadir, "postmaster.pid"), "w") as fh:
        fh.write("%d\n%s\n%d\n127.0.0.1:%d\n/tmp/.s.PGSQL.%d\n"
                 % (pid, dd or datadir, 1700000000000, port, port))


class ParityVacuousFail(unittest.TestCase):
    """parity.py must refuse captures that measured nothing."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.pgdir = os.path.join(self.tmp.name, "pgplans")
        os.mkdir(self.pgdir)

    def test_all_error_capture_exits_2(self):
        cap = write(self.tmp.name, "cap.txt", ALL_ERROR_CAPTURE)
        r = run_parity(cap, self.pgdir)
        self.assertEqual(r.returncode, 2, r.stdout + r.stderr)
        self.assertIn("nodes scored: 0", r.stdout)
        self.assertIn("vacuous", r.stdout)
        self.assertIn("ERROR", r.stdout)

    def test_no_sections_capture_exits_2(self):
        cap = write(self.tmp.name, "cap.txt", NO_SECTIONS_CAPTURE)
        r = run_parity(cap, self.pgdir)
        self.assertEqual(r.returncode, 2, r.stdout + r.stderr)

    def test_empty_capture_exits_2(self):
        cap = write(self.tmp.name, "cap.txt", "")
        r = run_parity(cap, self.pgdir)
        self.assertEqual(r.returncode, 2, r.stdout + r.stderr)

    def test_vacuous_capture_cannot_write_baseline(self):
        """The 2026-09-28 incident: a repin over a dead capture clobbered the
        pinned baseline with zero entries."""
        cap = write(self.tmp.name, "cap.txt", ALL_ERROR_CAPTURE)
        base = os.path.join(self.tmp.name, "baseline.txt")
        r = run_parity(cap, self.pgdir, ["--write-baseline", base])
        self.assertEqual(r.returncode, 2, r.stdout + r.stderr)
        self.assertFalse(os.path.exists(base),
                         "a 0-scored capture wrote a baseline file")

    def test_vacuous_capture_cannot_ratchet_pass(self):
        cap = write(self.tmp.name, "cap.txt", ALL_ERROR_CAPTURE)
        base = write(self.tmp.name, "baseline.txt", "Q1:store_sales\n")
        r = run_parity(cap, self.pgdir, ["--baseline", base])
        self.assertEqual(r.returncode, 2, r.stdout + r.stderr)
        self.assertNotIn("EA-RATCHET: PASS", r.stdout)

    def test_valid_capture_scores_and_exits_0(self):
        cap = write(self.tmp.name, "cap.txt", VALID_CAPTURE)
        r = run_parity(cap, self.pgdir)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("nodes scored: 2", r.stdout)

    def test_valid_capture_writes_baseline(self):
        cap = write(self.tmp.name, "cap.txt", VALID_CAPTURE)
        base = os.path.join(self.tmp.name, "baseline.txt")
        r = run_parity(cap, self.pgdir, ["--write-baseline", base])
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertTrue(os.path.exists(base))

    def test_partial_error_capture_is_scorable(self):
        """A corpus with some errored queries still measures something —
        only the ALL-error shape is vacuous."""
        partial = VALID_CAPTURE + "\n===== Q3 =====\npsql:ea-q.sql:1: ERROR:  boom\n"
        cap = write(self.tmp.name, "cap.txt", partial)
        r = run_parity(cap, self.pgdir)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("nodes scored: 2", r.stdout)


class GateVerifyServer(unittest.TestCase):
    """EA_VERIFY_ONLY=1 exercises the real verify_server in the gate."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.data = os.path.join(self.tmp.name, "data")
        os.mkdir(self.data)

    def gate(self, port=59999):
        return run_gate({"EA_DATA": self.data, "EA_PORT": str(port)})

    def test_no_pidfile_fails(self):
        r = self.gate()
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertIn("verify FAILED", r.stderr)

    def test_dead_pid_fails(self):
        write_pidfile(self.data, 99999999, 59999)
        r = self.gate()
        self.assertEqual(r.returncode, 2, r.stderr)

    def test_live_non_goopg_pid_fails(self):
        """A pidfile naming a live process that is not `goopg start -D`
        (this test's own python) must not verify — the stale-pidfile /
        recycled-pid shape."""
        write_pidfile(self.data, os.getpid(), 59999)
        r = self.gate()
        self.assertEqual(r.returncode, 2, r.stderr)

    def test_malformed_pidfile_fails(self):
        with open(os.path.join(self.data, "postmaster.pid"), "w") as fh:
            fh.write("garbage\n")
        r = self.gate()
        self.assertEqual(r.returncode, 2, r.stderr)


class GateCaptureMode(unittest.TestCase):
    """EA_CAPTURE mode wires the vacuous check end-to-end: no server, just
    the scorer over a committed capture file."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.pgdir = os.path.join(self.tmp.name, "pgplans")
        os.mkdir(self.pgdir)

    def gate(self, cap, extra=None):
        env = {"EA_CAPTURE": cap, "EA_PGDIR": self.pgdir,
               "EA_BASELINE": os.path.join(self.tmp.name, "no-baseline.txt"),
               "EA_VERIFY_ONLY": ""}
        if extra:
            env.update(extra)
        return run_gate(env)

    def test_all_error_capture_fails(self):
        cap = write(self.tmp.name, "cap.txt", ALL_ERROR_CAPTURE)
        r = self.gate(cap)
        self.assertEqual(r.returncode, 2, r.stdout + r.stderr)
        self.assertNotIn("EA-RATCHET: PASS", r.stdout)

    def test_valid_capture_reports(self):
        cap = write(self.tmp.name, "cap.txt", VALID_CAPTURE)
        r = self.gate(cap)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("nodes scored: 2", r.stdout)


if __name__ == "__main__":
    unittest.main(verbosity="-v" in sys.argv and 2 or 1)
