"""Tests for the core repair engine."""

from __future__ import annotations

import unittest

from tools.mdtablefix.models import Cell, Fix, Row, Table
from tools.mdtablefix.repair import repair_table
from tools.mdtablefix.tokenizer import tokenize_row


def _make_table(data_cells: list[list[str]], n_cols: int = 3) -> Table:
    """Build a minimal Table for testing repairs."""
    header = Row(
        cells=[Cell(content=f"h{i}") for i in range(n_cols)],
        line_number=1,
    )
    sep = Row(
        cells=[Cell(content="---") for _ in range(n_cols)],
        line_number=2,
    )
    data_rows = [
        Row(
            cells=[Cell(content=c) for c in row_cells],
            line_number=3 + idx,
        )
        for idx, row_cells in enumerate(data_cells)
    ]
    return Table(
        header=header,
        separator=sep,
        data_rows=data_rows,
        alignment=["left"] * n_cols,
        fixes=[],
        start_line=1,
    )


class TestRepairOversplit(unittest.TestCase):
    """Tests for oversplit (too-many-column) repair."""

    def test_oversplit_merged_right_to_left(self):
        """9 cells for a 7-column table: rightmost 3 merged into 1."""
        table = _make_table(
            [["a", "b", "c", "d", "e", "f", "g", "h", "i"]],
            n_cols=7,
        )
        repaired = repair_table(table)
        self.assertEqual(len(repaired.data_rows[0].cells), 7)
        # Last cell should contain merged content with escaped pipes.
        last_cell = repaired.data_rows[0].cells[-1]
        self.assertIn("\\|", last_cell.content)
        self.assertIn("g", last_cell.content)
        self.assertIn("h", last_cell.content)
        self.assertIn("i", last_cell.content)

    def test_oversplit_single_excess(self):
        """8 cells for a 7-column table."""
        table = _make_table(
            [["a", "b", "c", "d", "e", "f", "g", "h"]],
            n_cols=7,
        )
        repaired = repair_table(table)
        self.assertEqual(len(repaired.data_rows[0].cells), 7)
        last = repaired.data_rows[0].cells[-1].content
        self.assertEqual(last, r"g \| h")

    def test_oversplit_records_fixes(self):
        table = _make_table(
            [["a", "b", "c", "d", "e", "f", "g", "h"]],
            n_cols=7,
        )
        repaired = repair_table(table)
        self.assertGreater(len(repaired.fixes), 0)
        self.assertEqual(repaired.fixes[0].type, "escaped_pipe")

    def test_valid_row_unchanged(self):
        table = _make_table(
            [["a", "b", "c"]],
            n_cols=3,
        )
        repaired = repair_table(table)
        self.assertEqual(len(repaired.data_rows[0].cells), 3)
        self.assertEqual(repaired.data_rows[0].cells[0].content, "a")
        self.assertEqual(len(repaired.fixes), 0)


class TestRepairUndersplit(unittest.TestCase):
    """Tests for undersplit (too-few-column) repair."""

    def test_undersplit_padded_with_empty_cells(self):
        """5 cells for a 7-column table."""
        table = _make_table(
            [["a", "b", "c", "d", "e"]],
            n_cols=7,
        )
        repaired = repair_table(table)
        self.assertEqual(len(repaired.data_rows[0].cells), 7)
        self.assertEqual(repaired.data_rows[0].cells[5].content, "")
        self.assertEqual(repaired.data_rows[0].cells[6].content, "")

    def test_undersplit_records_fixes(self):
        table = _make_table(
            [["a", "b", "c", "d", "e"]],
            n_cols=7,
        )
        repaired = repair_table(table)
        missing_fixes = [f for f in repaired.fixes if f.type == "missing_cell"]
        self.assertEqual(len(missing_fixes), 2)

    def test_mixed_valid_and_malformed_rows(self):
        table = _make_table(
            [
                ["a", "b", "c", "d", "e", "f", "g"],       # valid
                ["a", "b", "c", "d", "e", "f", "g", "h"],   # oversplit
                ["a", "b", "c", "d", "e"],                   # undersplit
            ],
            n_cols=7,
        )
        repaired = repair_table(table)
        self.assertEqual(len(repaired.data_rows[0].cells), 7)  # valid
        self.assertEqual(len(repaired.data_rows[1].cells), 7)  # repaired
        self.assertEqual(len(repaired.data_rows[2].cells), 7)  # repaired


class TestRepairWithBacktickContent(unittest.TestCase):
    """Tests that backtick-delimited content is preserved during repair."""

    def test_backtick_content_preserved_in_merge(self):
        """Merging cells with backtick content preserves the backticks."""
        cells_raw = tokenize_row(
            "| a | b | c | `d|e` | f | g | h | i |"
        )
        row = Row(cells=cells_raw, line_number=3)
        header = Row(
            cells=[Cell(content=f"h{i}") for i in range(7)],
            line_number=1,
        )
        sep = Row(
            cells=[Cell(content="---") for _ in range(7)],
            line_number=2,
        )
        table = Table(
            header=header,
            separator=sep,
            data_rows=[row],
            alignment=["left"] * 7,
            fixes=[],
            start_line=1,
        )
        repaired = repair_table(table)
        # The backtick content `d|e` should be in cell [3] (0-indexed),
        # which is one of the "keep" cells (not merged).
        self.assertIn("`d|e`", repaired.data_rows[0].cells[3].content)
        # Cell [5] is "g" (also a keep cell).
        self.assertEqual("g", repaired.data_rows[0].cells[5].content)
        # The last cell should be the merged cells h and i.
        last_cell = repaired.data_rows[0].cells[-1].content
        self.assertIn("h", last_cell)
        self.assertIn("i", last_cell)


if __name__ == "__main__":
    unittest.main()
