"""Go source file collection with configurable exclusions (spec §4)."""

from __future__ import annotations

import fnmatch
import os

from .config import Config


def collect_go_files(config: Config, base: str = ".") -> list[str]:
    """Walk ``config.roots`` under ``base`` and return production ``*.go`` files.

    Excludes any directory whose name matches ``config.exclude_dirs`` and any
    file matching ``config.exclude_patterns`` (e.g. ``*_test.go``). Paths are
    returned relative to ``base`` and sorted for deterministic output (spec §7).
    """
    exclude_dirs = set(config.exclude_dirs)
    patterns = config.exclude_patterns
    found: list[str] = []

    for root in config.roots:
        root_path = os.path.join(base, root)
        if os.path.isfile(root_path):
            rel = os.path.relpath(root_path, base)
            if root_path.endswith(".go") and not _pattern_excluded(rel, patterns):
                found.append(rel)
            continue

        for dirpath, dirnames, filenames in os.walk(root_path):
            # Prune excluded directories in place so os.walk skips them.
            dirnames[:] = sorted(d for d in dirnames if d not in exclude_dirs)
            for name in filenames:
                if not name.endswith(".go"):
                    continue
                if _pattern_excluded(name, patterns):
                    continue
                rel = os.path.relpath(os.path.join(dirpath, name), base)
                found.append(rel)

    return sorted(set(found))


def _pattern_excluded(name: str, patterns: list[str]) -> bool:
    """True if ``name`` (basename or relative path) matches any glob pattern."""
    base = os.path.basename(name)
    return any(fnmatch.fnmatch(base, p) or fnmatch.fnmatch(name, p) for p in patterns)
