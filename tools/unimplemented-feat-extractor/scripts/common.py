"""Shared helpers for the unimplemented-feature extraction pipeline."""
import json
import re
import subprocess
import time
from pathlib import Path

REPO = Path("/home/ryo/work/goopg/goopg")
HERE = Path(__file__).parent
HAIKU = "claude-haiku-4-5-20251001"

WORK_UNIT_RE = re.compile(
    r"\b(M\d{4}-\d{4}[a-z]?|root-\d{4}|\d{4}-\d{4}[a-z]?|D[UW]?-\d{3}|[A-Z]{1,2}-\d{3}|slice \d+|loop #\d+)\b"
)


def load_commits():
    with open(REPO / "commit-log.json") as f:
        return json.load(f)["commits"]


def strip_fences(text: str) -> str:
    text = text.strip()
    m = re.search(r"```(?:json)?\s*(.*?)```", text, re.DOTALL)
    if m:
        return m.group(1).strip()
    return text


def haiku_json(prompt: str, retries: int = 2, timeout: int = 240):
    """Call headless haiku, return parsed JSON from its reply (None on failure)."""
    for attempt in range(retries + 1):
        try:
            proc = subprocess.run(
                ["claude", "-p", "--model", HAIKU, "--output-format", "json"],
                input=prompt, capture_output=True, text=True, timeout=timeout,
            )
            if proc.returncode != 0:
                raise RuntimeError(
                    f"claude exit {proc.returncode}: "
                    f"err={proc.stderr[:200]} out={proc.stdout[:200]}")
            envelope = json.loads(proc.stdout)
            if envelope.get("is_error"):
                raise RuntimeError(f"is_error: {envelope.get('result', '')[:300]}")
            return json.loads(strip_fences(envelope["result"]))
        except Exception as e:  # noqa: BLE001
            if attempt == retries:
                print(f"  haiku FAILED after {retries + 1} tries: {e}")
                return None
            time.sleep(15 * (attempt + 1))
    return None


def save(name: str, obj):
    with open(HERE / name, "w") as f:
        json.dump(obj, f, ensure_ascii=False, indent=1)
    print(f"wrote {name}")


def load(name: str):
    with open(HERE / name) as f:
        return json.load(f)
