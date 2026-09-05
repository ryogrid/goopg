"""Invoke gocyclo / gocognit and parse their output into FunctionMetric records.

Both tools share the same one-line output format::

    <metric> <package> <function> <file>:<line>:<col>

where ``<function>`` is a single space-free token (a receiver like
``(*Context).Foo`` contains no spaces). We therefore split on whitespace with a
maxsplit of 3 and split the trailing location on colons. The ``:col`` field is
occasionally absent: a ``//line`` directive (goyacc's ``//line yaccpar:1``
skeleton) leaves ``Column == 0``, which ``token.Position.String()`` renders as
``file:line``.
"""

from __future__ import annotations

import os
import shutil
import subprocess
from dataclasses import dataclass

from .models import FunctionMetric


class ToolNotFoundError(RuntimeError):
    """Raised when a required external binary is not on PATH."""


@dataclass(frozen=True)
class _RawRecord:
    metric: int
    package: str
    function: str
    file: str
    line: int


# go install hints surfaced when a binary is missing.
_INSTALL_HINTS = {
    "gocyclo": "go install github.com/fzipp/gocyclo/cmd/gocyclo@latest",
    "gocognit": "go install github.com/uudashr/gocognit/cmd/gocognit@latest",
}


def _require(tool: str) -> str:
    """Return the resolved path to ``tool`` or raise with an install hint."""
    path = shutil.which(tool)
    if path is None:
        hint = _INSTALL_HINTS.get(tool, "")
        raise ToolNotFoundError(
            f"required tool {tool!r} not found on PATH. Install it with:\n    {hint}"
        )
    return path


def parse_line(line: str) -> _RawRecord | None:
    """Parse one gocyclo/gocognit output line; return ``None`` for blank lines.

    Raises:
        ValueError: If a non-blank line is not in the expected 4-field format.
    """
    line = line.rstrip("\n")
    if not line.strip():
        return None
    parts = line.split(None, 3)
    if len(parts) != 4:
        raise ValueError(f"unparseable tool output line: {line!r}")
    metric_s, package, function, location = parts
    file, lineno = _split_location(location)
    return _RawRecord(
        metric=int(metric_s),
        package=package,
        function=function,
        file=file,
        line=lineno,
    )


def _split_location(location: str) -> tuple[str, int]:
    """Split ``file:line[:col]`` into ``(file, line)`` from the right.

    gocyclo/gocognit normally emit ``file:line:col``, but a ``//line`` directive
    (goyacc's ``//line yaccpar:1`` skeleton) can leave the column zero, which
    ``token.Position.String()`` renders as ``file:line`` with no trailing
    column. File paths never contain a colon in this codebase, so we only need
    to tolerate the missing third field.
    """
    parts = location.split(":")
    if len(parts) == 2:
        file, line_s = parts  # column omitted (Column == 0)
    elif len(parts) == 3:
        file, line_s, _col = parts
    else:
        raise ValueError(f"unparseable location: {location!r}")
    return file, int(line_s)


def _run_tool(tool: str, files: list[str], cwd: str) -> list[_RawRecord]:
    """Run ``tool`` over ``files`` and parse every output line."""
    binary = _require(tool)
    # gocyclo/gocognit sort their own output; we re-key by location afterwards.
    proc = subprocess.run(
        [binary, *files],
        cwd=cwd,
        capture_output=True,
        text=True,
        check=False,
    )
    if proc.returncode not in (0,) and not proc.stdout:
        # A non-zero exit with no stdout is a genuine failure (bad args, etc.).
        raise RuntimeError(
            f"{tool} failed (exit {proc.returncode}): {proc.stderr.strip()}"
        )
    records: list[_RawRecord] = []
    for line in proc.stdout.splitlines():
        rec = parse_line(line)
        if rec is not None:
            records.append(rec)
    return records


def analyze(files: list[str], cwd: str = ".") -> list[FunctionMetric]:
    """Run both tools over ``files`` and join their results per function.

    The cyclomatic pass drives the row set; cognitive values are looked up by
    ``(file, line, function)`` and left ``None`` when absent.
    """
    if not files:
        return []

    cyclo = _run_tool("gocyclo", files, cwd)
    cognit = _run_tool("gocognit", files, cwd)

    cognit_by_key: dict[tuple[str, int, str], int] = {
        (r.file, r.line, r.function): r.metric for r in cognit
    }

    metrics: list[FunctionMetric] = []
    for r in cyclo:
        key = (r.file, r.line, r.function)
        metrics.append(
            FunctionMetric(
                cyclomatic=r.metric,
                cognitive=cognit_by_key.get(key),
                package=r.package,
                function=r.function,
                file=r.file,
                line=r.line,
                directory=os.path.dirname(r.file) or ".",
            )
        )

    # Deterministic order: by file then line (spec §7).
    metrics.sort(key=lambda m: (m.file, m.line, m.function))
    return metrics
