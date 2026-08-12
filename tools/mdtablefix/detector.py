"""Table structure validation.

Determines whether a block of consecutive ``|``-lines is a valid
GFM table and extracts its alignment markers and column count.
"""

from __future__ import annotations

import re

from .models import Fix, Row, Table
from .tokenizer import tokenize_row


# A separator cell must consist only of ``-``, optional ``:``, and spaces.
_SEP_CELL_RE = re.compile(r"^:?-+:?$")


def normalize_outer_pipes(line: str) -> tuple[str, bool]:
    """Restore a row's missing leading / trailing ``|`` markers.

    GFM makes the outer pipes of a table row optional, so a row written
    ``| a | b | c`` (trailing pipe forgotten) is still a legal row and
    still renders.  The rest of this tool — ``_get_column_count_from_separator``,
    ``_parse_alignment``, and the formatter — assumes both markers are
    present, so every row is normalised here first.

    A trailing ``\\|`` does NOT count as an outer marker: it is an escaped
    literal pipe belonging to the last cell.

    Args:
        line: A raw table row.

    Returns:
        ``(normalized_line, changed)`` where *changed* is True if a marker
        had to be added.
    """
    stripped = line.strip()
    if not stripped:
        return stripped, False

    changed = False
    if not stripped.startswith("|"):
        stripped = "| " + stripped
        changed = True
    if not stripped.endswith("|") or stripped.endswith("\\|"):
        stripped = stripped + " |"
        changed = True
    return stripped, changed


def is_separator_row(line: str) -> bool:
    """Return True if *line* is a valid GFM separator row.

    Each cell (after stripping outer pipes) must match ``:?-+:?``
    (optional leading/trailing colon, one or more dashes, optional
    leading/trailing whitespace).  Missing outer pipes are tolerated
    (GFM makes them optional), but the line must contain at least one
    ``|`` so that a bare ``---`` — a thematic break or setext underline,
    not a table delimiter — is never mistaken for a separator row.
    """
    stripped = line.strip()
    if "|" not in stripped:
        return False
    stripped, _ = normalize_outer_pipes(stripped)

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
    stripped, _ = normalize_outer_pipes(separator_line)
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
    stripped, _ = normalize_outer_pipes(line)
    parts = stripped.split("|")
    inner = parts[1:-1] if len(parts) > 2 else []
    return len(inner)


def detect_table(
    lines: list[str], start_line: int
) -> Table | None:
    """Validate a table candidate and return a Table if valid.

    Two *structural* defects are repaired here rather than rejected, because
    both occur in hand-maintained ledgers and both used to disqualify the
    whole block — which made the tool silently report "no issues" on a file
    whose table was visibly broken:

    1. **Blank lines inside the body.**  A blank line terminates a table in
       GFM, so everything after it renders as literal ``|``-text.  Such lines
       are dropped (and recorded as ``blank_line`` fixes).
    2. **Missing outer pipes on a row.**  GFM makes the leading/trailing
       ``|`` optional, so ``| a | b | c`` is a legal row.  It is normalised
       (and recorded as a ``missing_outer_pipe`` fix) instead of poisoning
       detection for every other row in the table.

    Requirements:
    1. At least 2 non-blank lines (header + separator).
    2. Every non-blank line contains at least one ``|`` (a line with no pipe
       at all is prose, and means this block is not a table).
    3. The second non-blank line is a valid separator row.
    4. The separator row has a consistent, non-zero column count.

    Args:
        lines: Candidate table lines (without trailing newlines).
        start_line: 1-based line number of the first line.

    Returns:
        A Table if valid, or None if the block is not a table.  The returned
        table's ``fixes`` already carries any structural repairs; the
        cell-level repair pass appends to that list.
    """
    # Pair each non-blank line with its original line number, remembering
    # where the body-splitting blank lines were.
    indexed: list[tuple[int, str]] = []
    blank_line_numbers: list[int] = []
    for idx, ln in enumerate(lines):
        if ln.strip() == "":
            blank_line_numbers.append(start_line + idx)
        else:
            indexed.append((start_line + idx, ln))

    if len(indexed) < 2:
        return None

    # Quick rejection: a line without any pipe is not a table row.
    for _, ln in indexed:
        if "|" not in ln:
            return None

    # Second non-blank line must be a valid separator.
    if not is_separator_row(indexed[1][1]):
        return None

    structural_fixes: list[Fix] = []

    def _normalize(line_no: int, line: str) -> str:
        normalized, changed = normalize_outer_pipes(line)
        if changed:
            structural_fixes.append(
                Fix(
                    type="missing_outer_pipe",
                    line=line_no,
                    column=1,
                    detail=(
                        "Restored the row's missing leading/trailing '|' "
                        "marker"
                    ),
                )
            )
        return normalized

    # Parse header and separator.
    header_line_no, header_line = indexed[0]
    sep_line_no, sep_line = indexed[1]
    header_line = _normalize(header_line_no, header_line)
    sep_line = _normalize(sep_line_no, sep_line)
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
        cells = tokenize_row(_normalize(line_no, ln))
        data_rows.append(Row(cells=cells, line_number=line_no))

    # A blank line only breaks rendering when table rows follow it; a blank
    # that trails the block is just the separation from the next paragraph.
    last_row_line = indexed[-1][0]
    for blank_no in blank_line_numbers:
        if blank_no >= last_row_line:
            continue
        structural_fixes.append(
            Fix(
                type="blank_line",
                line=blank_no,
                column=1,
                detail=(
                    "Removed a blank line inside the table body — in GFM it "
                    "terminates the table, so every following row rendered "
                    "as literal text"
                ),
            )
        )
    structural_fixes.sort(key=lambda f: (f.line, f.column))

    return Table(
        header=header,
        separator=separator,
        data_rows=data_rows,
        alignment=alignment,
        fixes=structural_fixes,
        start_line=header_line_no,
    )
