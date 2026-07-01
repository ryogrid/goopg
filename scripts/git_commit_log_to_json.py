#!/usr/bin/env python3
"""Export this repository's git commit log to JSON.

Emits every non-merge commit as an object under a top-level ``commits`` array.
Each object has the keys ``commit_id``, ``title``, ``commited_at`` and
``description``. JSON escaping is delegated to the stdlib ``json`` module, so
message contents (newlines, quotes, unicode) are encoded correctly.

Usage:
    python3 scripts/git_commit_log_to_json.py [output_path]

``output_path`` defaults to ``commit-log.json`` at the repository root, so the
script is reusable and can be re-run at any time to regenerate the file from any
working directory.
"""

import json
import os
import re
import subprocess
import sys

# Field separator (ASCII unit separator) and record separator (NUL). Neither
# appears in git commit metadata or messages, so parsing never collides with
# content such as newlines inside a commit body.
FIELD_SEP = "\x1f"
RECORD_SEP = "\0"

# Trailing "Co-Authored-By:" attribution line (case-insensitive) that must be
# removed from the description.
COAUTHOR_RE = re.compile(r"(?i)^co-authored-by:")


def git_repo_root():
    """Return the absolute path to the repository root."""
    return subprocess.run(
        ["git", "rev-parse", "--show-toplevel"],
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()


def read_commits():
    """Return the raw git log for all non-merge commits as one string."""
    fmt = FIELD_SEP.join(["%H", "%s", "%cI", "%B"])
    result = subprocess.run(
        ["git", "log", "--no-merges", "-z", "--format=" + fmt],
        check=True,
        capture_output=True,
        text=True,
    )
    return result.stdout


def build_description(full_message):
    """Extract line 2 onward, stripping the trailing author attribution.

    ``full_message`` is the raw commit message (``%B``). The result is
    everything after the first line, with any trailing ``Co-Authored-By:``
    line(s) and the blank separator line above them removed, plus trailing
    whitespace trimmed.
    """
    # "2行目以降": everything after the first newline. Commits with only a
    # subject line have no remainder.
    parts = full_message.split("\n", 1)
    body = parts[1] if len(parts) > 1 else ""

    lines = body.split("\n")
    # Drop trailing empty lines and the trailing author-attribution block.
    while lines:
        last = lines[-1]
        if last.strip() == "" or COAUTHOR_RE.match(last):
            lines.pop()
        else:
            break
    return "\n".join(lines).strip()


def main():
    raw = read_commits()

    commits = []
    for record in raw.split(RECORD_SEP):
        if not record:
            continue
        commit_id, title, commited_at, full_message = record.split(FIELD_SEP, 3)
        commits.append(
            {
                "commit_id": commit_id,
                "title": title,
                "commited_at": commited_at,
                "description": build_description(full_message),
            }
        )

    if len(sys.argv) > 1:
        out_path = sys.argv[1]
    else:
        out_path = os.path.join(git_repo_root(), "commit-log.json")

    with open(out_path, "w", encoding="utf-8") as f:
        json.dump({"commits": commits}, f, ensure_ascii=False, indent=2)
        f.write("\n")

    print(f"Wrote {len(commits)} commits to {out_path}")


if __name__ == "__main__":
    main()
