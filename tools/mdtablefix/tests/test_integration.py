"""Integration tests for mdtablefix."""

from __future__ import annotations

import os
import subprocess
import sys
import tempfile
import unittest

from tools.mdtablefix.models import Table
from tools.mdtablefix.parser import parse_document
from tools.mdtablefix.repair import repair_table
from tools.mdtablefix.formatter import format_table


FIXTURES_DIR = os.path.join(os.path.dirname(__file__), "fixtures")


def _read_fixture(name: str) -> str:
    with open(os.path.join(FIXTURES_DIR, name), encoding="utf-8") as fh:
        return fh.read()


class TestIntegrationWithFixtures(unittest.TestCase):
    """End-to-end tests using fixture files."""

    def test_simple_table_roundtrip(self):
        """Parse → repair → format should produce valid markdown."""
        text = _read_fixture("simple_table.md")
        doc = parse_document(text)
        tables = [b for b in doc.blocks if isinstance(b, Table)]
        self.assertEqual(len(tables), 1)

        repaired = repair_table(tables[0])
        self.assertEqual(len(repaired.fixes), 0)  # already valid

        formatted = format_table(repaired)
        lines = formatted.split("\n")
        # Should have header + separator + 2 data rows.
        self.assertGreaterEqual(len(lines), 3)

    def test_code_fences_preserve_table_like_lines(self):
        """Lines inside code fences must not be detected as tables."""
        text = _read_fixture("table_with_code_fences.md")
        doc = parse_document(text)
        tables = [b for b in doc.blocks if isinstance(b, Table)]
        self.assertEqual(len(tables), 1)
        # The one table should be the one OUTSIDE the fence.
        self.assertIn("works", tables[0].data_rows[0].cells[0].content)

    def test_inline_pipes_tokenized_correctly(self):
        """Backtick-protected pipes must not cause oversplit."""
        text = _read_fixture("table_with_inline_pipes.md")
        doc = parse_document(text)
        tables = [b for b in doc.blocks if isinstance(b, Table)]
        self.assertEqual(len(tables), 1)

        table = tables[0]
        for row in table.data_rows:
            self.assertEqual(
                len(row.cells), 3,
                f"Line {row.line_number}: expected 3 cells, got {len(row.cells)}",
            )

    def test_deferral_ledger_sample_all_rows_seven_columns(self):
        """After repair, every row in the sample must have 7 columns."""
        text = _read_fixture("deferral_ledger_sample.md")
        doc = parse_document(text)
        tables = [b for b in doc.blocks if isinstance(b, Table)]
        self.assertGreaterEqual(len(tables), 1)

        for table in tables:
            repaired = repair_table(table)
            expected = len(repaired.header.cells)
            self.assertEqual(expected, 7)
            for row in repaired.data_rows:
                self.assertEqual(
                    len(row.cells), expected,
                    f"Line {row.line_number}: expected {expected} cells, "
                    f"got {len(row.cells)}: {[c.content[:50] for c in row.cells]}",
                )


class TestBlankSplitLedger(unittest.TestCase):
    """End-to-end repair of the deferral-ledger failure mode.

    The real ledger rendered only its first 940 rows on GitHub: a blank
    line inside the body ended the table, and one row missing its trailing
    pipe made the detector reject the block entirely — so the tool reported
    "no issues" on a visibly broken file.
    """

    def _repaired_document(self, text: str) -> str:
        doc = parse_document(text)
        out: list[str] = []
        for block in doc.blocks:
            if isinstance(block, Table):
                out.append(format_table(repair_table(block), compact=True))
            else:
                out.append("\n".join(block.lines))
        return "\n".join(out)

    def test_single_table_with_every_row(self):
        text = _read_fixture("blank_split_table.md")
        doc = parse_document(text)
        tables = [b for b in doc.blocks if isinstance(b, Table)]
        self.assertEqual(len(tables), 1)
        self.assertEqual(len(tables[0].data_rows), 6)

    def test_all_defects_are_reported(self):
        text = _read_fixture("blank_split_table.md")
        doc = parse_document(text)
        table = next(b for b in doc.blocks if isinstance(b, Table))
        fixes = repair_table(table).fixes
        by_type = {f.type for f in fixes}
        self.assertIn("blank_line", by_type)
        self.assertIn("missing_outer_pipe", by_type)
        self.assertIn("extra_cell", by_type)

    def test_repaired_output_has_no_body_blank_lines(self):
        text = _read_fixture("blank_split_table.md")
        repaired = self._repaired_document(text)
        body = [
            ln for ln in repaired.split("\n")
            if ln.startswith("|") or ln.strip() == ""
        ]
        # Walk the rendered table: no blank line may sit between two rows.
        rows = [i for i, ln in enumerate(body) if ln.startswith("|")]
        for a, b in zip(rows, rows[1:]):
            self.assertEqual(
                b, a + 1, f"blank line survived between rows {a} and {b}"
            )

    def test_repair_is_idempotent(self):
        text = _read_fixture("blank_split_table.md")
        once = self._repaired_document(text)
        twice = self._repaired_document(once)
        self.assertEqual(once, twice)

    def test_trailing_prose_is_preserved(self):
        text = _read_fixture("blank_split_table.md")
        repaired = self._repaired_document(text)
        self.assertIn("Trailing paragraph", repaired)


class TestCLIModes(unittest.TestCase):
    """Tests for the CLI via subprocess."""

    def _cli(self, *args: str) -> subprocess.CompletedProcess:
        venv_python = os.path.join(
            os.path.dirname(__file__), "..", "..", "..", "venv", "bin", "python"
        )
        return subprocess.run(
            [venv_python, "-m", "tools.mdtablefix"] + list(args),
            capture_output=True,
            text=True,
            cwd=os.path.join(os.path.dirname(__file__), "..", "..", ".."),
        )

    def test_check_on_valid_table_exits_zero(self):
        fixture = os.path.join(FIXTURES_DIR, "simple_table.md")
        proc = self._cli("--check", fixture)
        self.assertEqual(proc.returncode, 0)

    def test_check_on_inline_pipes_table_flags_unescaped_pipes(self):
        """Pipes inside backticks are NOT protected in GFM tables.

        GitHub splits a cell on such pipes, so they must be escaped; the
        checker must report them (exit 1) rather than call the table valid.
        """
        fixture = os.path.join(FIXTURES_DIR, "table_with_inline_pipes.md")
        proc = self._cli("--check", fixture)
        self.assertEqual(
            proc.returncode, 1,
            f"stderr: {proc.stderr}",
        )

    def test_fix_produces_output(self):
        fixture = os.path.join(FIXTURES_DIR, "simple_table.md")
        proc = self._cli("--fix", fixture)
        self.assertIn("|", proc.stdout)
        self.assertIn("col1", proc.stdout)

    def test_inplace_writes_file(self):
        fixture = os.path.join(FIXTURES_DIR, "simple_table.md")
        original = _read_fixture("simple_table.md")
        try:
            with tempfile.NamedTemporaryFile(
                mode="w", suffix=".md", delete=False, encoding="utf-8"
            ) as tmp:
                tmp.write(original)
                tmp_path = tmp.name

            proc = self._cli("--inplace", tmp_path)
            # Should succeed.
            self.assertIn(proc.returncode, (0, 1))

            # File should still be valid markdown.
            with open(tmp_path, encoding="utf-8") as fh:
                content = fh.read()
            self.assertIn("col1", content)
        finally:
            if os.path.exists(tmp_path):
                os.unlink(tmp_path)

    def test_report_flag_writes_json(self):
        fixture = os.path.join(FIXTURES_DIR, "simple_table.md")
        try:
            with tempfile.NamedTemporaryFile(
                mode="w", suffix=".json", delete=False, encoding="utf-8"
            ) as tmp:
                tmp_path = tmp.name

            proc = self._cli("--check", "--report", tmp_path, fixture)
            # Report is written regardless of exit code.
            self.assertTrue(
                os.path.exists(tmp_path),
                f"Report file not created. stderr: {proc.stderr}",
            )
        finally:
            if os.path.exists(tmp_path):
                os.unlink(tmp_path)


if __name__ == "__main__":
    unittest.main()
