"""Unit tests for the gocyclo/gocognit output parser."""

from __future__ import annotations

import unittest

from tools.gocomplexity.runner import _split_location, parse_line


class ParseLineTest(unittest.TestCase):
    def test_plain_function(self) -> None:
        rec = parse_line("6 executor Foo internal/executor/a.go:12:1")
        assert rec is not None
        self.assertEqual(rec.metric, 6)
        self.assertEqual(rec.package, "executor")
        self.assertEqual(rec.function, "Foo")
        self.assertEqual(rec.file, "internal/executor/a.go")
        self.assertEqual(rec.line, 12)

    def test_method_receiver_has_no_spaces(self) -> None:
        line = "16 executor (*Context).waitForRelationLockers internal/executor/context.go:1243:1"
        rec = parse_line(line)
        assert rec is not None
        self.assertEqual(rec.metric, 16)
        self.assertEqual(rec.function, "(*Context).waitForRelationLockers")
        self.assertEqual(rec.file, "internal/executor/context.go")
        self.assertEqual(rec.line, 1243)

    def test_blank_line_returns_none(self) -> None:
        self.assertIsNone(parse_line(""))
        self.assertIsNone(parse_line("   \n"))

    def test_malformed_line_raises(self) -> None:
        with self.assertRaises(ValueError):
            parse_line("not enough fields")

    def test_split_location_from_right(self) -> None:
        self.assertEqual(_split_location("a/b/c.go:99:3"), ("a/b/c.go", 99))


if __name__ == "__main__":
    unittest.main()
