"""Core repair engine for malformed markdown tables.

Repair strategies:
- **Oversplit** (too many columns): greedily merge excess cells
  right-to-left, escaping the pipe between merged cells as ``\\|``.
- **Undersplit** (too few columns): either a *lost separator* — a ``|``
  deleted from the middle of the row, which merged two columns into one
  over-long cell — or a genuinely short row.  The first is re-split at the
  word boundary the neighbouring rows' column widths point at; the second
  is padded with trailing empty cells.
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
    has_code_span,
    is_structural_html,
    raw_html_tag_names,
    separator_space_positions,
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
    """Repair a short row — by re-splitting it, or by padding it.

    Padding is right for a row an author simply ended early.  It is WRONG
    for a row whose separator was deleted mid-line: there the text of two
    columns sits fused in one cell, and appending an empty cell at the end
    changes nothing about how GitHub renders it (GFM pads short rows by
    itself), so the "repair" would only silence the report.

    A fused row is therefore re-split (see ``_plan_lost_separator``).  Only
    when no boundary fits well enough is the row left byte-identical and
    reported as needing a human — that finding then survives into every
    later run instead of being papered over.
    """
    plan = _plan_lost_separator(row, expected, profile)
    if plan is not None:
        idx, left, right = plan
        row.cells[idx] = Cell(content=left, is_code=has_code_span(left))
        row.cells.insert(idx + 1, Cell(content=right, is_code=has_code_span(right)))
        table.fixes.append(
            Fix(
                type="lost_separator",
                line=row.line_number,
                column=idx + 1,
                detail=(
                    f"Re-inserted the `|` lost between columns {idx + 1} and "
                    f"{idx + 2}; the boundary is placed where this table's "
                    "own column widths say it belongs — CHECK IT: "
                    f"…{_excerpt(left, 40, tail=True)} | "
                    f"{_excerpt(right, 40, head=True)}…"
                ),
            )
        )
        return

    fused = _find_fused_cell(row, expected, profile)
    if fused is not None:
        table.fixes.append(
            Fix(
                type="lost_separator",
                line=row.line_number,
                column=fused + 1,
                detail=(
                    f"Column {fused + 1} and {fused + 2} are fused into one "
                    "cell — a `|` separator is missing, not a trailing cell "
                    "— but no word boundary in it matches the widths of the "
                    "other rows, so the split point had to be left to a "
                    f"human. Cell: {_excerpt(row.cells[fused].content)}"
                ),
                repaired=False,
            )
        )
        return

    missing = expected - len(row.cells)
    for _ in range(missing):
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


def _find_fused_cell(
    row: Row, expected: int, profile: _ColumnProfile
) -> int | None:
    """Return the index of the cell that swallowed a separator, or None.

    Two independent signals must agree, so an ordinary short row is still
    padded silently:

    1. Some cell is longer than its own column plus a real share of the
       next column — i.e. it looks like two columns fused.
    2. The column that padding would leave empty is normally populated,
       so an empty cell there would be an anomaly in its own right.

    The widest offender relative to its column wins.
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


def _plan_lost_separator(
    row: Row, expected: int, profile: _ColumnProfile
) -> tuple[int, str, str] | None:
    """Locate the lost ``|`` and return (cell index, left text, right text).

    The generator that drops a separator drops the ``|`` and its padding
    together, so the boundary survives only as ordinary whitespace between
    two words — there is no marker left to find.  What IS left is the rest
    of the table: every other row says how wide these two columns run.  So
    each space in the fused cell is scored by how far the two halves it
    would produce sit from those two medians, and the best-scoring space is
    the boundary.

    The score is in characters, so the acceptance bar is stated the same
    way: the winning split must land within ~30% of the pair's combined
    width (never tighter than 24 characters, which keeps narrow columns
    from being impossible to satisfy).  On the row this was written for the
    winner scored 2 against a bar of 51, with the runner-up six times
    worse — a fit that loose would have to be, since a table whose columns
    have no typical width cannot be reconstructed by any means.

    Returns None when nothing clears the bar; the caller then reports the
    row instead of guessing.
    """
    fused = _find_fused_cell(row, expected, profile)
    if fused is None:
        return None

    med_left = profile.median_len[fused]
    med_right = profile.median_len[fused + 1]
    split = _best_split(row.cells[fused].content, med_left, med_right)
    if split is None:
        return None

    cost, left, right = split
    if cost > max(24.0, 0.30 * (med_left + med_right)):
        return None
    return fused, left, right


def _best_split(
    text: str, med_left: float, med_right: float
) -> tuple[float, str, str] | None:
    """Return (cost, left, right) for the best word boundary in *text*.

    Cost is the total character-count deviation of the two halves from the
    two column medians.  Boundaries inside backtick spans or inside an
    escaped ``\\|`` are not candidates, and a split that would empty either
    half is rejected.
    """
    best: tuple[float, str, str] | None = None
    for pos in separator_space_positions(text):
        left = text[:pos].rstrip()
        right = text[pos:].lstrip()
        if not left or not right:
            continue
        cost = abs(len(left) - med_left) + abs(len(right) - med_right)
        if best is None or cost < best[0]:
            best = (cost, left, right)
    return best


def _excerpt(
    text: str, width: int = 60, tail: bool = False, head: bool = False
) -> str:
    """Return a short, single-line quotation of *text* for a message.

    With *tail* / *head* one end is shown verbatim instead of an elided
    middle — used to print the two sides of a reconstructed boundary, where
    only the characters adjoining it matter.
    """
    flat = " ".join(text.split())
    if tail:
        return flat[-width:]
    if head:
        return flat[:width]
    if len(flat) <= width:
        return repr(flat)
    half = width // 2 - 2
    return repr(flat[:half] + " … " + flat[-half:])
