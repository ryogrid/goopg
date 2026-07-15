"""Tests for the table formatter."""

from __future__ import annotations

import unittest

from tools.mdtablefix.formatter import _compute_column_widths, _pad_cell, format_table
from tools.mdtablefix.models import Cell, Row, Table
from tools.mdtablefix.tokenizer import tokenize_row


def _make_simple_table() -> Table:
    """Build a simple 3-column table with alignment markers."""
    header = Row(
        cells=tokenize_row("| Name | Age | City |"),
        line_number=1,
    )
    sep = Row(
        cells=tokenize_row("| :--- | :---: | ---: |"),
        line_number=2,
    )
    data_rows = [
        Row(cells=tokenize_row("| Alice | 30 | Tokyo |"), line_number=3),
        Row(cells=tokenize_row("| Bob | 25 | Osaka |"), line_number=4),
    ]
    return Table(
        header=header,
        separator=sep,
        data_rows=data_rows,
        alignment=["left", "center", "right"],
        fixes=[],
        start_line=1,
    )


class TestPadCell(unittest.TestCase):
    """Tests for _pad_cell()."""

    def test_left_align(self):
        self.assertEqual(_pad_cell("ab", 5, "left"), "ab   ")

    def test_right_align(self):
        self.assertEqual(_pad_cell("ab", 5, "right"), "   ab")

    def test_center_align(self):
        result = _pad_cell("ab", 6, "center")
        self.assertEqual(len(result), 6)
        self.assertEqual(result, "  ab  ")

    def test_no_padding_needed(self):
        self.assertEqual(_pad_cell("abcde", 5, "left"), "abcde")


class TestComputeColumnWidths(unittest.TestCase):
    """Tests for _compute_column_widths()."""

    def test_minimum_width_is_three(self):
        table = _make_simple_table()
        widths = _compute_column_widths(table)
        for w in widths:
            self.assertGreaterEqual(w, 3)

    def test_width_matches_longest_cell(self):
        table = _make_simple_table()
        widths = _compute_column_widths(table)
        # "Alice" is 5 chars → width at least 5 (but min is 3, so >= 5).
        self.assertGreaterEqual(widths[0], 5)


class TestFormatTable(unittest.TestCase):
    """Tests for format_table()."""

    def test_output_is_valid_markdown(self):
        table = _make_simple_table()
        output = format_table(table)
        lines = output.split("\n")
        self.assertEqual(len(lines), 4)  # header + separator + 2 data rows
        for line in lines:
            self.assertTrue(line.startswith("|"))
            self.assertTrue(line.endswith("|"))

    def test_alignment_markers_preserved(self):
        table = _make_simple_table()
        output = format_table(table)
        sep_line = output.split("\n")[1]
        # Should contain alignment markers.
        self.assertIn(":", sep_line)

    def test_escaped_pipes_preserved_in_output(self):
        cells = tokenize_row("| a | b \\| c | d |")
        row = Row(cells=cells, line_number=3)
        header = Row(
            cells=[Cell(content=f"h{i}") for i in range(3)],
            line_number=1,
        )
        sep = Row(
            cells=[Cell(content="---") for _ in range(3)],
            line_number=2,
        )
        table = Table(
            header=header,
            separator=sep,
            data_rows=[row],
            alignment=["left"] * 3,
            fixes=[],
            start_line=1,
        )
        output = format_table(table)
        self.assertIn(r"b \| c", output)


if __name__ == "__main__":
    unittest.main()
