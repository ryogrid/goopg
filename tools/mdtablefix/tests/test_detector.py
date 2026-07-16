"""Tests for table structure detection."""

from __future__ import annotations

import unittest

from tools.mdtablefix.detector import (
    _get_column_count_from_separator,
    _parse_alignment,
    detect_table,
    is_separator_row,
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


if __name__ == "__main__":
    unittest.main()
