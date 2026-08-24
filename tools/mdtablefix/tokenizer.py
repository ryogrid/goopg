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


# ---------------------------------------------------------------------------
# Raw-HTML escaping
# ---------------------------------------------------------------------------
#
# A GFM table cell is inline content, so a ``<tag>`` written in prose is
# parsed as *raw HTML*, not as text.  Two things then go wrong on GitHub:
#
# * A placeholder whose name is not a real element — ``base/<db>/2611``,
#   ``REINDEX INDEX <name>`` — is dropped by GitHub's HTML sanitizer, so the
#   rendered cell silently loses the word.
# * A placeholder whose name *is* a real element — ``<table>_<col>_check`` —
#   is kept, and an unbalanced ``<table>`` opens a NESTED table inside the
#   cell that swallows every following row of the outer table.  One such
#   cell breaks the whole rest of the document's rendering.
#
# The repair is to write the ``<`` as ``&lt;``: GitHub then renders the
# placeholder literally and no element is opened.  Backtick spans are left
# alone — inline code is parsed before raw HTML, so ``` `<table>` ``` is
# already safe.

# Inline elements an author may legitimately hand-write inside a cell.
_INLINE_HTML_ALLOWLIST = frozenset(
    {
        "a", "b", "br", "code", "del", "em", "i", "img", "ins", "kbd",
        "mark", "s", "small", "strong", "sub", "sup", "u",
    }
)

# CommonMark raw-HTML forms: open/close tag, comment, processing
# instruction, declaration, CDATA.
_HTML_LIKE = re.compile(
    r"</?[A-Za-z][A-Za-z0-9-]*(?:\s[^<>]*?)?/?>"
    r"|<!--.*?-->"
    r"|<[?!][^>]*>",
    re.DOTALL,
)

# An autolink (``<https://…>``, ``<user@host>``) is legitimate markdown and
# must survive untouched, even though it matches the tag pattern above.
_AUTOLINK = re.compile(
    r"<[A-Za-z][A-Za-z0-9+.\-]{1,31}:[^<>\s]*>"
    r"|<[^<>\s@]+@[^<>\s@.]+\.[^<>\s@]+>"
)

_TAG_NAME = re.compile(r"</?([A-Za-z][A-Za-z0-9-]*)")


# Elements GitHub's sanitizer keeps.  A stray one of these does not just
# vanish — it opens a real element inside the cell, and an unbalanced
# ``<table>`` swallows every following row of the outer table.
_SANITIZER_KEEPS = frozenset(
    {
        "table", "thead", "tbody", "tfoot", "tr", "td", "th", "col",
        "colgroup", "caption", "div", "p", "span", "ul", "ol", "li", "dl",
        "dt", "dd", "pre", "blockquote", "details", "summary", "hr",
        "h1", "h2", "h3", "h4", "h5", "h6", "figure", "figcaption",
    }
)


def raw_html_tag_names(cell_text: str) -> list[str]:
    """Return the lowercased names of the tags ``escape_html_tags`` rewrites.

    Callers use it to say *why* a cell is broken: a name in
    ``_SANITIZER_KEEPS`` opens an element that damages the rows after it,
    while any other name is silently deleted from the rendered cell.
    """
    names: list[str] = []
    for start, text in _escapable_html_spans(cell_text):
        del start
        name_match = _TAG_NAME.match(text)
        names.append(name_match.group(1).lower() if name_match else "")
    return names


def is_structural_html(name: str) -> bool:
    """Return True if *name* survives GitHub's sanitizer as a real element."""
    return name in _SANITIZER_KEEPS


def _escapable_html_spans(cell_text: str) -> list[tuple[int, str]]:
    """Return (offset, text) for every raw-HTML run that must be escaped."""
    if "<" not in cell_text:
        return []

    protected = _find_backtick_spans(cell_text)
    found: list[tuple[int, str]] = []
    for match in _HTML_LIKE.finditer(cell_text):
        start = match.start()
        if any(lo <= start < hi for lo, hi in protected):
            continue
        if _AUTOLINK.match(cell_text, start, match.end()):
            continue
        name_match = _TAG_NAME.match(match.group(0))
        if name_match and name_match.group(1).lower() in _INLINE_HTML_ALLOWLIST:
            continue
        found.append((start, match.group(0)))
    return found


def escape_html_tags(cell_text: str) -> str:
    """Escape raw-HTML-looking ``<…>`` runs in *cell_text* as ``&lt;…>``.

    Only the opening ``<`` is rewritten; a bare ``>`` carries no meaning in
    inline context, so leaving it alone keeps the diff minimal and makes the
    operation idempotent (``&lt;table>`` no longer matches).

    Left untouched: backtick code spans, markdown autolinks, and the
    hand-written inline elements in ``_INLINE_HTML_ALLOWLIST``.
    """
    escape_at = [start for start, _ in _escapable_html_spans(cell_text)]
    if not escape_at:
        return cell_text

    out: list[str] = []
    prev = 0
    for pos in escape_at:
        out.append(cell_text[prev:pos])
        out.append("&lt;")
        prev = pos + 1
    out.append(cell_text[prev:])
    return "".join(out)


def has_code_span(text: str) -> bool:
    """Return True if *text* contains at least one backtick code span."""
    return bool(_find_backtick_spans(text))


def separator_space_positions(text: str) -> list[int]:
    """Return the offsets of spaces where a lost ``|`` could be re-inserted.

    A well-formed separator is written `` | ``, so when the ``|`` alone is
    dropped the boundary survives as ordinary whitespace between two words.
    Only spaces OUTSIDE backtick spans qualify: a separator can never have
    stood inside `` `code` ``, and splitting there would tear a code span in
    half.  Spaces belonging to an escaped ``\\|`` are excluded for the same
    reason.
    """
    protected = _find_backtick_spans(text)
    escaped = _find_escaped_pipe_positions(text)
    blocked = set()
    for pos in escaped:
        blocked.update({pos - 1, pos, pos + 1, pos + 2})

    positions: list[int] = []
    for idx, ch in enumerate(text):
        if ch not in " \t":
            continue
        if idx in blocked:
            continue
        if any(lo <= idx < hi for lo, hi in protected):
            continue
        positions.append(idx)
    return positions
