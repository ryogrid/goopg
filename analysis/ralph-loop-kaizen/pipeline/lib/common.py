"""Shared helpers for the ralph-loop-kaizen preprocessing pipeline.

All stages import from here. The design goal is CHEAP-FIRST: everything in this
module is pure-Python stdlib (no LLM, no third-party deps) so the structured
passes cost nothing to run.

Two on-disk log formats exist and both are handled transparently:

  * "result" format  - a single JSON object (one line), ``type == "result"``.
    Used by the ralph driver for ~all loops up to ~2026-06-02.
  * "stream" format  - JSONL of streaming events (``stream_event``/``assistant``/
    ``user``/``system``/``result``). Used by ``*_stream.log`` for ~all history,
    and by the main ``.log`` for the last ~1 day after the driver switched to
    ``--output-format stream-json``.

The result summary object (cost/tokens/turns) appears as the whole file in the
"result" format and as the trailing ``type=="result"`` line in the "stream"
format, so :func:`extract_result_object` finds it in either.
"""

from __future__ import annotations

import json
import math
import os
import re
from datetime import datetime, timezone
from pathlib import Path

# ---------------------------------------------------------------------------
# Path resolution
# ---------------------------------------------------------------------------

# pipeline/lib/common.py -> pipeline -> ralph-loop-kaizen -> analysis -> repo root
REPO_ROOT = Path(__file__).resolve().parents[4]


def repo_root(override: str | None = None) -> Path:
    if override:
        return Path(override).resolve()
    env = os.environ.get("KAIZEN_REPO_ROOT")
    return Path(env).resolve() if env else REPO_ROOT


def logs_dir(repo: Path, override: str | None = None) -> Path:
    if override:
        return Path(override).resolve()
    return repo / ".ralph" / "logs"


# ---------------------------------------------------------------------------
# Log-file discovery and parsing
# ---------------------------------------------------------------------------

_TS_RE = re.compile(r"claude_output_(\d{4}-\d{2}-\d{2})_(\d{2}-\d{2}-\d{2})")


def loop_log_files(logs: Path, stream: bool = False) -> list[Path]:
    """Sorted list of per-loop log files.

    ``stream=False`` returns the main ``claude_output_*.log`` (excluding the
    ``_stream`` siblings); ``stream=True`` returns the ``*_stream.log`` files.
    """
    files = sorted(logs.glob("claude_output_*.log"))
    if stream:
        return [f for f in files if f.name.endswith("_stream.log")]
    return [f for f in files if not f.name.endswith("_stream.log")]


def filename_timestamp(path: Path) -> datetime | None:
    """Parse the loop start time encoded in the log filename (naive UTC)."""
    m = _TS_RE.search(path.name)
    if not m:
        return None
    try:
        return datetime.strptime(f"{m.group(1)} {m.group(2)}", "%Y-%m-%d %H-%M-%S").replace(
            tzinfo=timezone.utc
        )
    except ValueError:
        return None


def iter_json_objects(path: Path):
    """Yield parsed JSON objects from a log file, format-agnostic.

    Streams line-by-line, which correctly handles BOTH loop-log formats: the
    "result" format is a single JSON object on one line, and the "stream" format
    is JSONL. Streaming keeps memory bounded even on the multi-100 MB stream
    logs (a whole-file ``read_text`` would hold 2-3x the file size). Malformed
    lines are skipped. As a fallback for a small pretty-printed single object
    (not a loop-log case, but cheap insurance), the whole file is parsed only if
    nothing parsed line-by-line and the file is small.
    """
    try:
        size = path.stat().st_size
    except OSError:
        return
    parsed_any = False
    try:
        with path.open(errors="replace") as fh:
            for line in fh:
                line = line.strip()
                if not line:
                    continue
                try:
                    obj = json.loads(line)
                except json.JSONDecodeError:
                    continue
                parsed_any = True
                yield obj
    except OSError:
        return
    if parsed_any or size > 5_000_000:
        return
    try:
        yield json.loads(path.read_text(errors="replace"))
    except (OSError, json.JSONDecodeError):
        pass


def extract_result_object(path: Path) -> dict | None:
    """Return the result-summary dict from a log file, or None if absent.

    Works for both formats. For stream logs we additionally synthesise a
    minimal summary from assistant messages when no explicit ``type==result``
    object is present (e.g. aborted loops).
    """
    result = None
    assistant_models: list[str] = []
    sum_output = 0
    sum_cache_creation = 0
    sum_input = 0
    last_cache_read = 0
    n_assistant = 0
    init_model = None
    # In the stream format a single logical assistant message is emitted as
    # MULTIPLE lines (one per content block), each repeating the same message.id
    # and the same usage object. Dedupe by message.id so we neither double-count
    # tokens nor inflate the turn count. (cache_read is taken as the last/peak
    # value, never summed, since each turn re-reads the growing cache.)
    seen_msg_ids: set = set()
    for obj in iter_json_objects(path):
        t = obj.get("type")
        if t == "result":
            result = obj
        elif t == "system" and obj.get("subtype") == "init":
            init_model = obj.get("model")
        elif t == "assistant":
            msg = obj.get("message", {}) or {}
            mid = msg.get("id")
            if mid is not None and mid in seen_msg_ids:
                continue
            if mid is not None:
                seen_msg_ids.add(mid)
            n_assistant += 1
            if msg.get("model"):
                assistant_models.append(msg["model"])
            u = msg.get("usage", {}) or {}
            sum_output += int(u.get("output_tokens") or 0)
            sum_cache_creation += int(u.get("cache_creation_input_tokens") or 0)
            sum_input += int(u.get("input_tokens") or 0)
            if u.get("cache_read_input_tokens"):
                last_cache_read = int(u["cache_read_input_tokens"])
    if result is not None:
        return result
    if n_assistant == 0:
        return None
    # Synthesise a summary for an aborted/stream-only loop. cache_read is the
    # last (approx peak) value; summing it across turns would massively
    # overcount because each turn re-reads the growing cache.
    return {
        "type": "result",
        "synthesised": True,
        "model": init_model or (assistant_models[-1] if assistant_models else None),
        "num_turns": n_assistant,
        "usage": {
            "output_tokens": sum_output,
            "input_tokens": sum_input,
            "cache_creation_input_tokens": sum_cache_creation,
            "cache_read_input_tokens": last_cache_read,
        },
    }


# ---------------------------------------------------------------------------
# RALPH_STATUS block (best-effort: present in only a minority of loops)
# ---------------------------------------------------------------------------

_STATUS_FIELDS = (
    "STATUS",
    "TASKS_COMPLETED_THIS_LOOP",
    "FILES_MODIFIED",
    "TESTS_STATUS",
    "WORK_TYPE",
    "EXIT_SIGNAL",
)


def parse_ralph_status(text: str) -> dict | None:
    """Extract the ``---RALPH_STATUS---`` block fields, or None if absent."""
    if not text or "RALPH_STATUS" not in text:
        return None
    out: dict[str, str] = {}
    for field in _STATUS_FIELDS:
        m = re.search(rf"^{field}:\s*(.+)$", text, re.MULTILINE)
        if m:
            out[field] = m.group(1).strip()
    return out or None


def result_text(result_obj: dict) -> str:
    """The free-text summary the agent emitted, if any."""
    return result_obj.get("result") or ""


# ---------------------------------------------------------------------------
# Numeric helpers
# ---------------------------------------------------------------------------

def to_int(v, default: int = 0) -> int:
    try:
        return int(v)
    except (TypeError, ValueError):
        return default


def to_float(v, default: float = 0.0) -> float:
    try:
        return float(v)
    except (TypeError, ValueError):
        return default


def percentiles(values: list[float], ps=(50, 90, 99)) -> dict:
    """Simple nearest-rank percentiles. Returns {} for empty input."""
    if not values:
        return {}
    s = sorted(values)
    out = {}
    n = len(s)
    for p in ps:
        # textbook nearest-rank: rank = ceil(p/100 * n), 1-based -> 0-based index
        k = max(0, min(n - 1, math.ceil(p / 100.0 * n) - 1))
        out[f"p{p}"] = s[k]
    out["min"] = s[0]
    out["max"] = s[-1]
    out["mean"] = sum(s) / n
    out["count"] = n
    return out


def read_metrics(logs: Path) -> list[dict]:
    """Parse metrics.jsonl (the per-loop spine)."""
    path = logs / "metrics.jsonl"
    rows: list[dict] = []
    if not path.exists():
        return rows
    for line in path.read_text(errors="replace").splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            rows.append(json.loads(line))
        except json.JSONDecodeError:
            continue
    return rows


def dump_json(obj, path: Path) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(obj, indent=2, default=str))
