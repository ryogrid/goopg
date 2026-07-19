"""Cell splitter and pipe-escaper for markdown table rows.

Two distinct concerns live here:

* **Splitting** (``tokenize_row``) recovers a row's *intended* columns.
  An author's real separators are the top-level, whitespace-padded ``|``
  markers outside any backtick span, so — purely as a structural heuristic —
  we mask backtick spans and tightly-wedged "prose" pipes, then split on the
  remaining pipes.

* **Escaping** (``escape_embedded_pipes``) makes a cell render correctly on
  GitHub.  Here the backtick masking does NOT apply: a GFM table row is
  split into cells *before* inline parsing, so the only literal pipe is a
  backslash-escaped ``\\|`` — a pipe inside `` `code` `` is still a column
  separator on GitHub.  Every bare pipe is therefore escaped, code or not.
"""

from __future__ import annotations

import re

from .models import Cell


def _find_backtick_spans(line: str) -> list[tuple[int, int]]:
    """Return (start, end) index pairs for each backtick-delimited span.

    Supports both single-backtick `` `code` `` and double-backtick
    ``` ``code`` ``` forms.  An unmatched opening backtick run is
    treated as literal text (no span emitted).
    """
    spans: list[tuple[int, int]] = []
    i = 0
    n = len(line)

    while i < n:
        if line[i] != "`":
            i += 1
            continue

        # Count consecutive backticks.
        bt_len = 1
        while i + bt_len < n and line[i + bt_len] == "`":
            bt_len += 1

        opener = "`" * bt_len
        close_pos = line.find(opener, i + bt_len)
        if close_pos != -1:
            # Include the backticks themselves in the span.
            spans.append((i, close_pos + bt_len))
            i = close_pos + bt_len
        else:
            # Unmatched — treat the backtick(s) as literal text.
            i += bt_len

    return spans


def _find_escaped_pipe_positions(line: str) -> set[int]:
    """Return the positions of ``\\`` characters that precede a ``|``.

    These are NOT column separators and must be kept verbatim.
    We return the index of the backslash so the masker can treat the
    two-byte ``\\|`` sequence as non-separator.
    """
    return {m.start() for m in re.finditer(r"\\\|", line)}


# Whitespace characters that, when they flank a run of pipes, mark it as a
# *column separator* run rather than a prose pipe.
_SEPARATOR_FLANK = frozenset(" \t")


def _is_prose_pipe(line: str, idx: int) -> bool:
    """Return True if the ``|`` at *idx* is a literal pipe, not a separator.

    Well-formed table cells are space-padded, so real column separators
    look like `` | `` (whitespace on at least one side) and the outer
    markers touch the string boundary.  Empty cells appear as a run of
    pipes flanked by whitespace, e.g. `` || `` or `` ||| ``.

    A run of pipes wedged tightly between two content characters — the
    single ``|`` in ``ADMIN|INHERIT`` or ``{a|b}``, or the ``||`` in a
    C-style expression like ``(!IsMatView||IsPopulated)`` — is prose that
    an author wrote without escaping, not a column boundary.  Splitting on
    such pipes oversplits the row and corrupts its real column structure,
    so we keep them inside the cell (they are escaped to ``\\|`` on output).

    The decision is made per *run*: we look past any adjacent pipes to the
    first non-pipe character on each side.  A run touching whitespace or a
    string boundary on either side is a separator; a run flanked by content
    characters on both sides is prose.
    """
    n = len(line)

    # Expand to the full contiguous run of pipes containing *idx*.
    run_start = idx
    while run_start > 0 and line[run_start - 1] == "|":
        run_start -= 1
    run_end = idx
    while run_end + 1 < n and line[run_end + 1] == "|":
        run_end += 1

    left = line[run_start - 1] if run_start > 0 else ""
    right = line[run_end + 1] if run_end + 1 < n else ""
    if left == "" or right == "":
        # The run touches a string boundary — an outer table marker.
        return False
    return left not in _SEPARATOR_FLANK and right not in _SEPARATOR_FLANK


def tokenize_row(line: str) -> list[Cell]:
    """Split a table row into cells, respecting backtick code spans.

    Pipes inside backtick-delimited spans and backslash-escaped pipes
    (``\\|``) are treated as literal content, not column separators.
    Leading and trailing empty cells (from the outer ``|`` markers)
    are stripped.

    Args:
        line: A raw table row, e.g. ``| a | `b|c` | d |``.

    Returns:
        List of Cell objects, one per column.
    """
    line = line.strip()

    # ---- locate protected regions ------------------------------------
    backtick_spans = _find_backtick_spans(line)
    escaped_pipe_positions = _find_escaped_pipe_positions(line)

    # Build a mask: '\x00' for protected characters, original char otherwise.
    masked = list(line)
    for start, end in backtick_spans:
        for j in range(start, end):
            masked[j] = "\x00"
    for pos in escaped_pipe_positions:
        # Mask both the backslash and the pipe.
        masked[pos] = "\x00"
        if pos + 1 < len(masked):
            masked[pos + 1] = "\x00"

    # ---- split on real (unmasked, non-prose) pipes --------------------
    # A pipe that is tightly wedged between two content characters (e.g.
    # ``ADMIN|INHERIT``) is a literal pipe an author forgot to escape, not
    # a column separator.  Treating it as a separator oversplits the row.
    real_pipe_positions = [
        idx
        for idx, ch in enumerate(masked)
        if ch == "|" and not _is_prose_pipe(line, idx)
    ]

    cells_raw: list[str] = []
    prev = 0
    for pos in real_pipe_positions:
        cells_raw.append(line[prev:pos].strip())
        prev = pos + 1
    cells_raw.append(line[prev:].strip())

    # ---- strip leading / trailing empty cells from outer pipes --------
    if cells_raw and cells_raw[0] == "":
        cells_raw.pop(0)
    if cells_raw and cells_raw[-1] == "":
        cells_raw.pop()

    # ---- build Cell objects -------------------------------------------
    result: list[Cell] = []
    for text in cells_raw:
        # Determine whether this cell contains backtick-delimited content.
        cell_backtick_spans = _find_backtick_spans(text)
        is_code = len(cell_backtick_spans) > 0
        result.append(Cell(content=text, is_code=is_code))

    return result


# Matches a bare ``|`` — one NOT already backslash-escaped as ``\|``.
_BARE_PIPE = re.compile(r"(?<!\\)\|")


def escape_embedded_pipes(cell_text: str) -> str:
    """Escape every literal ``|`` in a cell as ``\\|``.

    In a GitHub-Flavored-Markdown *table*, a cell is split on pipes
    **before** its inline content is parsed, so ONLY a backslash-escaped
    ``\\|`` counts as a literal pipe.  Crucially, backtick code spans do
    **not** protect pipes here: ``| `a|b` |`` renders as two columns on
    GitHub, not one cell containing ``a|b``.  A literal pipe inside code
    must therefore also be written ``\\|`` — GitHub strips the backslash
    when extracting the cell, so `` `if v \\| w` `` renders as
    ``<code>if v | w</code>`` in a single cell.

    We escape every bare pipe wherever it appears (prose or code alike).
    Already-escaped ``\\|`` sequences are preserved (not double-escaped),
    which makes the operation idempotent.
    """
    return _BARE_PIPE.sub(r"\\|", cell_text)
