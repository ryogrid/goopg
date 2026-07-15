"""Table structure validation.

Determines whether a block of consecutive ``|``-lines is a valid
GFM table and extracts its alignment markers and column count.
"""

from __future__ import annotations

import re

from .models import Row, Table
from .tokenizer import tokenize_row


# A separator cell must consist only of ``-``, optional ``:``, and spaces.
_SEP_CELL_RE = re.compile(r"^:?-+:?$")


def is_separator_row(line: str) -> bool:
    """Return True if *line* is a valid GFM separator row.

    Each cell (after stripping outer pipes) must match ``:?-+:?``
    (optional leading/trailing colon, one or more dashes, optional
    leading/trailing whitespace).
    """
    stripped = line.strip()
    if not stripped.startswith("|") or not stripped.endswith("|"):
        return False

    # Split naively — separator rows never contain backtick-protected pipes.
    parts = stripped.split("|")
    # Strip leading/trailing empty strings from outer pipes.
    inner = parts[1:-1] if len(parts) > 2 else []
    if not inner:
        return False

    for cell in inner:
        if not _SEP_CELL_RE.fullmatch(cell.strip()):
            return False

    return True


def _parse_alignment(separator_line: str) -> list[str]:
    """Extract per-column alignment from a separator row.

    Returns a list of ``"left"``, ``"right"``, or ``"center"`` strings.
    """
    stripped = separator_line.strip()
    parts = stripped.split("|")
    inner = parts[1:-1] if len(parts) > 2 else []

    alignment: list[str] = []
    for cell in inner:
        cell = cell.strip()
        left = cell.startswith(":")
        right = cell.endswith(":")
        if left and right:
            alignment.append("center")
        elif right:
            alignment.append("right")
        else:
            alignment.append("left")
    return alignment


def _get_column_count_from_separator(line: str) -> int:
    """Count columns from a separator row by naive pipe-splitting."""
    stripped = line.strip()
    parts = stripped.split("|")
    inner = parts[1:-1] if len(parts) > 2 else []
    return len(inner)


def detect_table(
    lines: list[str], start_line: int
) -> Table | None:
    """Validate a table candidate and return a Table if valid.

    Blank lines within the table body are silently skipped (they are
    formatting errors in GFM — a blank line terminates a table).

    Requirements:
    1. At least 2 non-blank lines (header + separator).
    2. All non-blank lines start and end with ``|``.
    3. The second non-blank line is a valid separator row.
    4. The separator row has a consistent, non-zero column count.

    Args:
        lines: Candidate table lines (without trailing newlines).
        start_line: 1-based line number of the first line.

    Returns:
        A Table if valid, or None if the block is not a table.
    """
    # Pair each non-blank line with its original line number.
    indexed: list[tuple[int, str]] = [
        (start_line + idx, ln)
        for idx, ln in enumerate(lines)
        if ln.strip() != ""
    ]

    if len(indexed) < 2:
        return None

    # Quick rejection: all non-blank lines must start and end with '|'.
    for _, ln in indexed:
        stripped = ln.strip()
        if not stripped.startswith("|") or not stripped.endswith("|"):
            return None

    # Second non-blank line must be a valid separator.
    if not is_separator_row(indexed[1][1]):
        return None

    # Parse header and separator.
    header_line_no, header_line = indexed[0]
    sep_line_no, sep_line = indexed[1]
    header_cells = tokenize_row(header_line)
    sep_cells = tokenize_row(sep_line)
    expected_cols = _get_column_count_from_separator(sep_line)

    if expected_cols < 1:
        return None

    # Verify header column count matches separator (backtick-protected).
    if len(header_cells) != expected_cols:
        return None

    alignment = _parse_alignment(sep_line)
    if len(alignment) != expected_cols:
        return None

    header = Row(cells=header_cells, line_number=header_line_no)
    separator = Row(cells=sep_cells, line_number=sep_line_no)

    # Parse data rows (all remaining indexed lines after the separator).
    data_rows: list[Row] = []
    for line_no, ln in indexed[2:]:
        cells = tokenize_row(ln)
        data_rows.append(Row(cells=cells, line_number=line_no))

    return Table(
        header=header,
        separator=separator,
        data_rows=data_rows,
        alignment=alignment,
        fixes=[],
        start_line=header_line_no,
    )
