"""Tests for table structure detection."""

from __future__ import annotations

import unittest

from tools.mdtablefix.detector import (
    _get_column_count_from_separator,
    _parse_alignment,
    detect_table,
    is_separator_row,
    normalize_outer_pipes,
)


class TestIsSeparatorRow(unittest.TestCase):
    """Tests for is_separator_row()."""

    def test_valid_separator(self):
        self.assertTrue(is_separator_row("| --- | --- | --- |"))

    def test_with_alignment_markers(self):
        self.assertTrue(is_separator_row("| :--- | :---: | ---: |"))

    def test_single_column(self):
        self.assertTrue(is_separator_row("| --- |"))

    def test_not_a_separator_missing_pipes(self):
        self.assertFalse(is_separator_row("hello world"))

    def test_not_a_separator_has_letters(self):
        self.assertFalse(is_separator_row("| abc | def |"))

    def test_separator_with_spaces(self):
        self.assertTrue(is_separator_row("| :--- | :---: | ---: |"))


class TestParseAlignment(unittest.TestCase):
    """Tests for _parse_alignment()."""

    def test_all_left(self):
        self.assertEqual(
            _parse_alignment("| --- | --- | --- |"),
            ["left", "left", "left"],
        )

    def test_mixed(self):
        self.assertEqual(
            _parse_alignment("| :--- | :---: | ---: |"),
            ["left", "center", "right"],
        )


class TestGetColumnCount(unittest.TestCase):
    """Tests for _get_column_count_from_separator()."""

    def test_three_columns(self):
        self.assertEqual(
            _get_column_count_from_separator("| --- | --- | --- |"),
            3,
        )

    def test_seven_columns(self):
        sep = "|--------|------|---------|--------|----------|--------------|-----|"
        self.assertEqual(_get_column_count_from_separator(sep), 7)


class TestDetectTable(unittest.TestCase):
    """Tests for detect_table()."""

    def test_valid_simple_table(self):
        lines = [
            "| a | b | c |",
            "| --- | --- | --- |",
            "| x | y | z |",
        ]
        table = detect_table(lines, start_line=1)
        self.assertIsNotNone(table)
        if table:
            self.assertEqual(len(table.header.cells), 3)
            self.assertEqual(len(table.data_rows), 1)

    def test_missing_separator(self):
        lines = [
            "| a | b | c |",
            "| x | y | z |",
        ]
        table = detect_table(lines, start_line=1)
        self.assertIsNone(table)

    def test_only_header_and_separator(self):
        lines = [
            "| a | b | c |",
            "| --- | --- | --- |",
        ]
        table = detect_table(lines, start_line=1)
        self.assertIsNotNone(table)
        if table:
            self.assertEqual(len(table.data_rows), 0)

    def test_non_table_lines(self):
        lines = [
            "hello world",
            "not a table",
        ]
        table = detect_table(lines, start_line=1)
        self.assertIsNone(table)

    def test_line_without_pipes(self):
        lines = [
            "| a | b |",
            "not a table row",
            "| x | y |",
        ]
        table = detect_table(lines, start_line=1)
        self.assertIsNone(table)

    def test_extracts_alignment(self):
        lines = [
            "| a | b | c |",
            "| :--- | :---: | ---: |",
            "| x | y | z |",
        ]
        table = detect_table(lines, start_line=1)
        self.assertIsNotNone(table)
        if table:
            self.assertEqual(table.alignment, ["left", "center", "right"])

    def test_line_numbers_are_one_based(self):
        lines = [
            "| a | b |",
            "| --- | --- |",
            "| x | y |",
        ]
        table = detect_table(lines, start_line=5)
        self.assertIsNotNone(table)
        if table:
            self.assertEqual(table.start_line, 5)
            self.assertEqual(table.header.line_number, 5)
            self.assertEqual(table.data_rows[0].line_number, 7)


class TestNormalizeOuterPipes(unittest.TestCase):
    """Tests for normalize_outer_pipes()."""

    def test_already_normal(self):
        self.assertEqual(
            normalize_outer_pipes("| a | b |"), ("| a | b |", False)
        )

    def test_missing_trailing_pipe(self):
        self.assertEqual(
            normalize_outer_pipes("| a | b"), ("| a | b |", True)
        )

    def test_missing_leading_pipe(self):
        self.assertEqual(
            normalize_outer_pipes("a | b |"), ("| a | b |", True)
        )

    def test_trailing_escaped_pipe_is_not_an_outer_marker(self):
        """``\\|`` ends a *cell*, so the row still needs its outer marker."""
        self.assertEqual(
            normalize_outer_pipes(r"| a | b \|"), (r"| a | b \| |", True)
        )


class TestDetectTableStructuralRepairs(unittest.TestCase):
    """Blank lines and missing outer pipes are repaired, not fatal.

    Both defects previously made detect_table() reject the whole block, so
    a document with one bad row was reported as having "no issues" while
    rendering visibly broken on GitHub.
    """

    def test_blank_line_in_body_is_absorbed(self):
        lines = [
            "| a | b |",
            "| --- | --- |",
            "| 1 | 2 |",
            "",
            "| 3 | 4 |",
        ]
        table = detect_table(lines, start_line=1)
        self.assertIsNotNone(table)
        assert table is not None
        self.assertEqual(len(table.data_rows), 2)
        blank_fixes = [f for f in table.fixes if f.type == "blank_line"]
        self.assertEqual(len(blank_fixes), 1)
        self.assertEqual(blank_fixes[0].line, 4)

    def test_trailing_blank_is_not_reported(self):
        """A blank after the last row separates blocks; it breaks nothing."""
        lines = ["| a | b |", "| --- | --- |", "| 1 | 2 |", ""]
        table = detect_table(lines, start_line=1)
        self.assertIsNotNone(table)
        assert table is not None
        self.assertEqual([f for f in table.fixes if f.type == "blank_line"], [])

    def test_row_missing_trailing_pipe_is_repaired(self):
        lines = ["| a | b |", "| --- | --- |", "| 1 | 2"]
        table = detect_table(lines, start_line=1)
        self.assertIsNotNone(table)
        assert table is not None
        self.assertEqual(len(table.data_rows[0].cells), 2)
        pipe_fixes = [f for f in table.fixes if f.type == "missing_outer_pipe"]
        self.assertEqual(len(pipe_fixes), 1)
        self.assertEqual(pipe_fixes[0].line, 3)

    def test_one_bad_row_does_not_disqualify_the_table(self):
        """The ledger's failure mode: 1 row of 1000 lacked a trailing pipe."""
        lines = ["| a | b |", "| --- | --- |"]
        lines += [f"| {i} | x |" for i in range(20)]
        lines[10] = "| 8 | no trailing pipe"
        table = detect_table(lines, start_line=1)
        self.assertIsNotNone(table)
        assert table is not None
        self.assertEqual(len(table.data_rows), 20)

    def test_separator_without_outer_pipes(self):
        lines = ["a | b", "--- | ---", "| 1 | 2 |"]
        table = detect_table(lines, start_line=1)
        self.assertIsNotNone(table)
        assert table is not None
        self.assertEqual(len(table.header.cells), 2)

    def test_bare_dashes_are_not_a_separator(self):
        """``---`` alone is a thematic break, not a table delimiter row."""
        self.assertFalse(is_separator_row("---"))
        self.assertIsNone(detect_table(["| a | b |", "---", "| 1 | 2 |"], 1))


if __name__ == "__main__":
    unittest.main()
