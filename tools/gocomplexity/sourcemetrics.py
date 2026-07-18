"""Source-level metrics: LOC, Halstead volume, Maintainability Index, duplication.

These complement the function-level complexity from gocyclo/gocognit. Everything
here is computed by a self-contained, dependency-free Go source scanner so it
honors exactly the same production-only file set (tests and utility tooling
already excluded by the collector) and stays deterministic.

The scanner is a lightweight hand-written lexer — good enough for line
classification and Halstead token counting, and intentionally approximate (it
does not build a full Go AST). Limitations are documented in README.md.
"""

from __future__ import annotations

import math
import os
from collections import defaultdict
from dataclasses import dataclass, field

# Go keywords are treated as Halstead *operators*; identifiers/literals are
# operands (the conventional split used by complexity tools).
GO_KEYWORDS: frozenset[str] = frozenset(
    """break case chan const continue default defer else fallthrough for func go
    goto if import interface map package range return select struct switch type
    var""".split()
)

# Multi-character operators/punctuation, longest first so we match greedily.
_OPERATORS: tuple[str, ...] = (
    "<<=", ">>=", "&^=", "...",
    "&&", "||", "<-", "++", "--", "==", "!=", "<=", ">=", ":=",
    "<<", ">>", "&^", "+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=",
    "+", "-", "*", "/", "%", "&", "|", "^", "<", ">", "=", "!",
    "(", ")", "[", "]", "{", "}", ",", ";", ".", ":", "~",
)


@dataclass
class Halstead:
    """Halstead operator/operand counts and derived volume."""

    n1: int = 0  # distinct operators
    n2: int = 0  # distinct operands
    N1: int = 0  # total operators
    N2: int = 0  # total operands

    @property
    def vocabulary(self) -> int:
        return self.n1 + self.n2

    @property
    def length(self) -> int:
        return self.N1 + self.N2

    @property
    def volume(self) -> float:
        n = self.vocabulary
        if n <= 0:
            return 0.0
        return self.length * math.log2(n)


@dataclass
class FileSource:
    """Per-file source metrics produced by the scanner."""

    file: str
    total_lines: int
    loc: int  # code lines (non-blank, non-comment)
    halstead: Halstead
    # code line number -> normalized token signature (for duplicate detection)
    norm_lines: dict[int, str] = field(default_factory=dict)


def _is_ident_start(c: str) -> bool:
    return c.isalpha() or c == "_" or ord(c) >= 0x80


def _is_ident_part(c: str) -> bool:
    return c.isalnum() or c == "_" or ord(c) >= 0x80


def scan_file(text: str) -> FileSource:
    """Scan Go source text into LOC, Halstead counts, and per-line signatures."""
    n = len(text)
    total_lines = text.count("\n") + 1 if text else 0
    has_code: set[int] = set()
    line_tokens: dict[int, list[str]] = defaultdict(list)

    op_counter: dict[str, int] = defaultdict(int)
    operand_counter: dict[str, int] = defaultdict(int)

    i = 0
    line = 1

    def emit(kind: str, norm: str, at_line: int) -> None:
        has_code.add(at_line)
        line_tokens[at_line].append(norm)

    while i < n:
        c = text[i]

        # Newline.
        if c == "\n":
            line += 1
            i += 1
            continue

        # Whitespace.
        if c in " \t\r\f\v":
            i += 1
            continue

        # Comments (not code).
        if c == "/" and i + 1 < n and text[i + 1] == "/":
            i += 2
            while i < n and text[i] != "\n":
                i += 1
            continue
        if c == "/" and i + 1 < n and text[i + 1] == "*":
            i += 2
            while i + 1 < n and not (text[i] == "*" and text[i + 1] == "/"):
                if text[i] == "\n":
                    line += 1
                i += 1
            i += 2  # consume closing */
            continue

        # Raw string literal (may span lines).
        if c == "`":
            start_line = line
            i += 1
            while i < n and text[i] != "`":
                if text[i] == "\n":
                    line += 1
                i += 1
            i += 1
            emit("string", '"s"', start_line)
            operand_counter['"s"'] += 1
            continue

        # Interpreted string literal.
        if c == '"':
            i += 1
            while i < n and text[i] != '"':
                if text[i] == "\\":
                    i += 1
                i += 1
            i += 1
            emit("string", '"s"', line)
            operand_counter['"s"'] += 1
            continue

        # Rune / char literal.
        if c == "'":
            i += 1
            while i < n and text[i] != "'":
                if text[i] == "\\":
                    i += 1
                i += 1
            i += 1
            emit("char", "'c'", line)
            operand_counter["'c'"] += 1
            continue

        # Number literal.
        if c.isdigit() or (c == "." and i + 1 < n and text[i + 1].isdigit()):
            j = i + 1
            while j < n and (
                text[j].isalnum() or text[j] in "._"
                or (text[j] in "+-" and text[j - 1] in "eEpP")
            ):
                j += 1
            i = j
            emit("number", "0", line)
            operand_counter["0"] += 1
            continue

        # Identifier or keyword.
        if _is_ident_start(c):
            j = i + 1
            while j < n and _is_ident_part(text[j]):
                j += 1
            word = text[i:j]
            i = j
            if word in GO_KEYWORDS:
                emit("op", word, line)
                op_counter[word] += 1
            else:
                emit("name", word, line)
                operand_counter[word] += 1
            continue

        # Operator / punctuation (longest match).
        matched = None
        for op in _OPERATORS:
            if text.startswith(op, i):
                matched = op
                break
        if matched is not None:
            emit("op", matched, line)
            op_counter[matched] += 1
            i += len(matched)
            continue

        # Unknown byte — skip without marking code.
        i += 1

    halstead = Halstead(
        n1=len(op_counter),
        n2=len(operand_counter),
        N1=sum(op_counter.values()),
        N2=sum(operand_counter.values()),
    )
    norm_lines = {ln: " ".join(toks) for ln, toks in line_tokens.items()}
    return FileSource(
        file="",
        total_lines=total_lines,
        loc=len(has_code),
        halstead=halstead,
        norm_lines=norm_lines,
    )


def scan_sources(files: list[str], base: str = ".") -> dict[str, FileSource]:
    """Scan every collected file; returns a mapping keyed by relative path."""
    result: dict[str, FileSource] = {}
    for rel in files:
        try:
            text = open(os.path.join(base, rel), encoding="utf-8", errors="replace").read()
        except OSError:
            continue
        fs = scan_file(text)
        fs.file = rel
        result[rel] = fs
    return result


def maintainability_index(volume: float, avg_cyclomatic: float, loc: float) -> float:
    """Coleman-Oman Maintainability Index, normalized to 0-100 (Microsoft variant).

    MI = max(0, min(100, (171 - 5.2*ln(V) - 0.23*CC - 16.2*ln(LOC)) * 100 / 171))

    The constants are calibrated for module-sized units, so callers pass the
    *per-function averages* for a file: ``volume`` = mean Halstead volume per
    function, ``avg_cyclomatic`` = mean cyclomatic complexity per function,
    ``loc`` = mean code lines per function. Terms with a non-positive log
    argument are dropped so the formula stays defined for tiny inputs.
    """
    if loc <= 0:
        return 100.0
    ln_v = math.log(volume) if volume > 0 else 0.0
    ln_loc = math.log(loc) if loc > 0 else 0.0
    raw = 171.0 - 5.2 * ln_v - 0.23 * avg_cyclomatic - 16.2 * ln_loc
    return max(0.0, min(100.0, raw * 100.0 / 171.0))


def duplication_pct(
    sources: dict[str, FileSource], min_lines: int = 6
) -> tuple[float, int, int]:
    """Estimate the fraction of code lines inside duplicated blocks.

    Slides a window of ``min_lines`` consecutive code lines within each file,
    hashes the normalized signatures, and flags every line covered by a window
    whose signature occurs at two or more distinct locations (in any file).

    Returns ``(percentage, duplicated_code_lines, total_code_lines)``. The
    percentage catches type-1 clones plus literal-normalized (type-2-lite)
    clones; it is deterministic (files iterated in sorted order).
    """
    # window signature -> list of (file, [line numbers]) occurrences
    windows: dict[tuple[str, ...], list[tuple[str, tuple[int, ...]]]] = defaultdict(list)
    total_code = 0

    for rel in sorted(sources):
        fs = sources[rel]
        code_lines = sorted(fs.norm_lines.keys())
        total_code += len(code_lines)
        if len(code_lines) < min_lines:
            continue
        sigs = [fs.norm_lines[ln] for ln in code_lines]
        for start in range(0, len(code_lines) - min_lines + 1):
            sig = tuple(sigs[start : start + min_lines])
            span = tuple(code_lines[start : start + min_lines])
            windows[sig].append((rel, span))

    duplicated: dict[str, set[int]] = defaultdict(set)
    for sig, occ in windows.items():
        if len(occ) < 2:
            continue
        for rel, span in occ:
            duplicated[rel].update(span)

    dup_lines = sum(len(s) for s in duplicated.values())
    pct = round(100.0 * dup_lines / total_code, 4) if total_code else 0.0
    return pct, dup_lines, total_code
