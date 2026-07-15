"""Backtick-aware cell splitter for markdown table rows.

The central insight: pipe characters (``|``) inside inline code spans
(backtick-delimited) are NOT column separators.  This module locates
backtick spans, masks their content, then splits the row on the
remaining (real) pipe characters.
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

    # ---- split on real (unmasked) pipes -------------------------------
    real_pipe_positions = [
        idx for idx, ch in enumerate(masked) if ch == "|"
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


def escape_embedded_pipes(cell_text: str) -> str:
    """Escape literal ``|`` as ``\\|``, but NOT inside backtick spans.

    Already-escaped ``\\|`` sequences are preserved (not double-escaped).
    Backtick-delimited code is passed through unchanged; only bare pipes
    in the surrounding markdown text are escaped.
    """
    backtick_spans = _find_backtick_spans(cell_text)

    def _escape_outside_backticks(text: str) -> str:
        """Escape bare ``|`` → ``\\|``, preserving existing ``\\|``."""
        # Protect already-escaped pipes, then escape bare pipes, then restore.
        text = text.replace("\\|", "\x01")
        text = text.replace("|", "\\|")
        text = text.replace("\x01", "\\|")
        return text

    if not backtick_spans:
        return _escape_outside_backticks(cell_text)

    # Build result piecewise, escaping only outside backtick spans.
    result_parts: list[str] = []
    prev_end = 0
    for start, end in backtick_spans:
        # Escape pipes in the text BEFORE this backtick span.
        before = cell_text[prev_end:start]
        result_parts.append(_escape_outside_backticks(before))
        # Pass the backtick span through unchanged.
        result_parts.append(cell_text[start:end])
        prev_end = end
    # Escape pipes in the trailing text after the last backtick span.
    result_parts.append(_escape_outside_backticks(cell_text[prev_end:]))

    return "".join(result_parts)
