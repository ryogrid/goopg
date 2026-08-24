"""Core repair engine for malformed markdown tables.

Repair strategies:
- **Oversplit** (too many columns): greedily merge excess cells
  right-to-left, escaping the pipe between merged cells as ``\\|``.
- **Undersplit** (too few columns): either a *lost separator* — a ``|``
  deleted from the middle of the row, which merged two columns into one
  over-long cell — or a genuinely short row.  The first is reported and
  left alone (the split point is unrecoverable), the second is padded with
  trailing empty cells.
- **Raw HTML**: a ``<tag>``-shaped run in a cell is entity-escaped so it
  renders literally instead of being swallowed (``<db>``) or opening a
  nested element that eats the rest of the table (``<table>``).
"""

from __future__ import annotations

import statistics

from .models import Cell, Fix, Row, Table
from .tokenizer import (
    escape_embedded_pipes,
    escape_html_tags,
    is_structural_html,
    raw_html_tag_names,
    tokenize_row,
)


def repair_table(table: Table) -> Table:
    """Repair a malformed table in-place (mutates and returns *table*).

    1. Determine the expected column count from the header row.
    2. For each data row whose column count differs from expected:
       - Too many → right-to-left greedy merge.
       - Too few  → trailing empty-cell padding.
    3. Record each repair in ``table.fixes``.

    Structural fixes already recorded by the detector (removed body-splitting
    blank lines, restored outer pipes) are preserved — they are part of the
    same repair and must survive into the report.
    """
    expected_cols = len(table.header.cells)
    profile = _column_profile(table, expected_cols)

    for row in table.data_rows:
        actual = len(row.cells)
        if actual == expected_cols:
            continue

        if actual > expected_cols:
            _repair_oversplit_row(row, expected_cols, table)
        else:
            _repair_undersplit_row(row, expected_cols, table, profile)

    # Normalize: escape any bare ``|`` still embedded in cell content.
    # These come from prose pipes the tokenizer intentionally kept inside a
    # cell (e.g. ``{ADMIN|INHERIT|SET}``); left un-escaped they would render
    # as spurious column separators on GitHub.  Escaping is idempotent —
    # backtick spans and existing ``\|`` are preserved — so already-correct
    # cells are untouched.
    for row in table.data_rows:
        _escape_bare_pipes_in_row(row, table)

    # Normalize: entity-escape raw ``<tag>`` runs.  Unlike a stray pipe this
    # damages the document *beyond* its own row — an unbalanced ``<table>``
    # nests every following row inside one cell — so the header is swept too.
    for row in [table.header, *table.data_rows]:
        _escape_html_in_row(row, table)

    return table


class _ColumnProfile:
    """Per-column shape of a table, measured on its well-formed rows.

    Used to tell a *lost separator* from a genuinely short row: both look
    identical structurally (n-1 cells), and only the surrounding rows say
    which one it is.
    """

    def __init__(self, median_len: list[float], populated: list[float]):
        self.median_len = median_len
        self.populated = populated

    def absorbed_next_column(self, col_idx: int, length: int) -> bool:
        """Is a cell of *length* too long to be column *col_idx* alone?

        A cell that swallowed the separator holds its own column's text
        plus the next column's.  Requiring at least half of the next
        column's typical width keeps ordinary long cells from tripping it.
        """
        if col_idx + 1 >= len(self.median_len):
            return False
        own = self.median_len[col_idx]
        nxt = self.median_len[col_idx + 1]
        return length > own + max(20.0, nxt / 2.0)


def _column_profile(table: Table, expected: int) -> _ColumnProfile:
    """Measure median cell width and populated-fraction per column."""
    samples: list[list[int]] = [[] for _ in range(expected)]
    for row in table.data_rows:
        if len(row.cells) != expected:
            continue
        for idx, cell in enumerate(row.cells):
            samples[idx].append(len(cell.content))

    median_len: list[float] = []
    populated: list[float] = []
    for lengths in samples:
        if not lengths:
            median_len.append(0.0)
            populated.append(0.0)
            continue
        median_len.append(float(statistics.median(lengths)))
        populated.append(sum(1 for n in lengths if n > 0) / len(lengths))
    return _ColumnProfile(median_len, populated)


def _escape_html_in_row(row: Row, table: Table) -> None:
    """Entity-escape raw ``<tag>`` runs in each cell of *row*."""
    for col_idx, cell in enumerate(row.cells):
        names = raw_html_tag_names(cell.content)
        if not names:
            continue
        escaped = escape_html_tags(cell.content)
        if escaped == cell.content:
            continue
        cell.content = escaped
        shown = ", ".join(f"<{n}>" if n else "<!…>" for n in names)
        structural = [n for n in names if is_structural_html(n)]
        if structural:
            why = (
                "GitHub keeps <"
                + structural[0]
                + "> as a real element, so it opens inside this cell and "
                "nests every following row of the table inside it"
            )
        else:
            why = (
                "GitHub's sanitizer deletes unknown tags, so this text "
                "disappears from the rendered cell"
            )
        table.fixes.append(
            Fix(
                type="html_tag",
                line=row.line_number,
                column=col_idx + 1,
                detail=(
                    f"Entity-escaped raw HTML in column {col_idx + 1} "
                    f"({shown}): {why}"
                ),
            )
        )


def _escape_bare_pipes_in_row(row: Row, table: Table) -> None:
    """Escape bare ``|`` in each cell of *row*, recording any change."""
    for col_idx, cell in enumerate(row.cells):
        escaped = escape_embedded_pipes(cell.content)
        if escaped != cell.content:
            cell.content = escaped
            table.fixes.append(
                Fix(
                    type="escaped_pipe",
                    line=row.line_number,
                    column=col_idx + 1,
                    detail=(
                        f"Escaped bare pipe(s) in column {col_idx + 1} "
                        "so they render as literal text, not separators"
                    ),
                )
            )


def _drop_excess_empty_cells(row: Row, expected: int, table: Table) -> None:
    """Drop empty excess cells, left to right, while the row is oversplit.

    The classic cause of a single excess cell is a doubled separator —
    ``| resolved | | 2026-08-11 | …`` — which shifts every following column
    one position right.  Merging the tail (the fallback strategy) would
    render correctly but leave the row's *meaning* scrambled, joining two
    unrelated columns.  An empty cell carries no content, so removing it is
    lossless and restores the intended column alignment instead.
    """
    excess = len(row.cells) - expected
    idx = 0
    while excess > 0 and idx < len(row.cells):
        if row.cells[idx].content == "":
            del row.cells[idx]
            excess -= 1
            table.fixes.append(
                Fix(
                    type="extra_cell",
                    line=row.line_number,
                    column=idx + 1,
                    detail=(
                        f"Dropped empty extra cell at column {idx + 1} "
                        "(doubled column separator); the following columns "
                        "shift back into place"
                    ),
                )
            )
            continue
        idx += 1


def _repair_oversplit_row(row: Row, expected: int, table: Table) -> None:
    """Merge excess cells into the rightmost columns.

    Empty excess cells are dropped first (see ``_drop_excess_empty_cells``);
    only content cells reach the merge below.

    The rightmost ``expected`` cells absorb all overflow from the
    extra cells.  The separating pipes are replaced with ``\\|``
    inside the merged cell content.

    Example with 9 cells, expected=7::

        [A, B, C, D, E, F, G, H, I]
        → merge H|I  →  [..., G, H\\|I]        (2 excess remaining)
        → merge G|H\\|I →  [..., F, G\\|H\\|I]  (1 excess remaining)
        → [A, B, C, D, E, F, G\\|H\\|I]         (done)
    """
    _drop_excess_empty_cells(row, expected, table)
    excess = len(row.cells) - expected
    if excess == 0:
        return

    # Keep the first (expected-1) cells untouched.
    # Merge the remaining (excess+1) cells into the last column.
    keep = expected - 1
    merge_group = row.cells[keep:]  # (excess + 1) cells

    # Join the merge-group cells, escaping embedded pipes.
    merged_parts: list[str] = []
    for cell in merge_group:
        merged_parts.append(escape_embedded_pipes(cell.content))
    merged_content = " \\| ".join(merged_parts)

    new_cells = row.cells[:keep]
    new_cells.append(Cell(content=merged_content, is_code=False))
    row.cells = new_cells

    # Record fixes.
    for idx in range(excess):
        col = keep + idx + 2  # 1-based column of the merged separator
        table.fixes.append(
            Fix(
                type="escaped_pipe",
                line=row.line_number,
                column=col,
                detail=(
                    f"Escaped pipe: merged extra column {col} "
                    f"into column {keep + 1}"
                ),
            )
        )


def _repair_undersplit_row(
    row: Row, expected: int, table: Table, profile: _ColumnProfile
) -> None:
    """Repair — or, for a lost separator, only report — a short row.

    Padding is right for a row an author simply ended early.  It is WRONG
    for a row whose separator was deleted mid-line: there the text of two
    columns sits fused in one cell, appending an empty cell at the end
    changes nothing about how GitHub renders it (GFM pads short rows by
    itself), and the "repair" merely silences the report.  That case is
    reported as an unrepairable ``lost_separator`` and the row is left
    byte-identical, so the finding survives into every later run.
    """
    lost_at = _find_lost_separator(row, expected, profile)
    if lost_at is not None:
        left = row.cells[lost_at].content
        table.fixes.append(
            Fix(
                type="lost_separator",
                line=row.line_number,
                column=lost_at + 1,
                detail=(
                    f"Column {lost_at + 1} and {lost_at + 2} are fused into "
                    "one cell — a `|` separator is missing, not a trailing "
                    "cell. Re-insert it by hand; the split point cannot be "
                    f"recovered from the text. Cell: {_excerpt(left)}"
                ),
                repaired=False,
            )
        )
        return

    missing = expected - len(row.cells)
    for idx in range(missing):
        col = len(row.cells) + 1
        row.cells.append(Cell(content="", is_code=False))
        table.fixes.append(
            Fix(
                type="missing_cell",
                line=row.line_number,
                column=col,
                detail=f"Added missing cell at column {col}",
            )
        )


def _find_lost_separator(
    row: Row, expected: int, profile: _ColumnProfile
) -> int | None:
    """Return the index of the cell that swallowed a separator, or None.

    Two independent signals must agree, so an ordinary short row is still
    padded silently:

    1. Some cell is longer than its own column plus a real share of the
       next column — i.e. it looks like two columns fused.
    2. The column that padding would leave empty is normally populated,
       so an empty cell there would be an anomaly in its own right.

    The widest offender relative to its column wins; ties do not matter
    because the finding is diagnostic, not a rewrite.
    """
    if len(row.cells) != expected - 1:
        return None
    if profile.populated[expected - 1] < 0.5:
        return None

    best: int | None = None
    best_excess = 0.0
    for idx, cell in enumerate(row.cells):
        length = len(cell.content)
        if not profile.absorbed_next_column(idx, length):
            continue
        excess = length - profile.median_len[idx]
        if excess > best_excess:
            best, best_excess = idx, excess
    return best


def _excerpt(text: str, width: int = 60) -> str:
    """Return a short, single-line quotation of *text* for a message."""
    flat = " ".join(text.split())
    if len(flat) <= width:
        return repr(flat)
    half = width // 2 - 2
    return repr(flat[:half] + " … " + flat[-half:])
