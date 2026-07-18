"""Configuration loading and defaults for gocomplexity.

Precedence (lowest to highest): built-in defaults < YAML config file < CLI flags.
The YAML file is optional; when absent the built-in defaults apply.
"""

from __future__ import annotations

from dataclasses import dataclass, field, replace
from pathlib import Path

import yaml

# Spec §4: directories excluded by default.
DEFAULT_EXCLUDE_DIRS: tuple[str, ...] = (
    "vendor",
    "third_party",
    "testutil",
    "testport",
    "tmp",
    "tools",
    "examples",
)

# Spec §4: filename patterns excluded by default.
DEFAULT_EXCLUDE_PATTERNS: tuple[str, ...] = ("*_test.go",)

# Default roots to analyze when the caller passes none.
DEFAULT_ROOTS: tuple[str, ...] = ("internal", "cmd")

# Complexity values above which functions are flagged/counted (spec §3).
DEFAULT_THRESHOLDS: tuple[int, ...] = (10, 15, 20, 30)


@dataclass
class Config:
    """Resolved configuration for a single analysis run."""

    roots: list[str] = field(default_factory=lambda: list(DEFAULT_ROOTS))
    exclude_dirs: list[str] = field(default_factory=lambda: list(DEFAULT_EXCLUDE_DIRS))
    exclude_patterns: list[str] = field(
        default_factory=lambda: list(DEFAULT_EXCLUDE_PATTERNS)
    )
    thresholds: list[int] = field(default_factory=lambda: list(DEFAULT_THRESHOLDS))
    top_functions: int = 100
    top_packages: int = 20
    top_files: int = 50

    def normalized(self) -> "Config":
        """Return a copy with de-duplicated, sorted threshold list."""
        return replace(self, thresholds=sorted(set(self.thresholds)))


def load_config(path: str | None) -> Config:
    """Load a :class:`Config`, merging an optional YAML file over the defaults.

    Args:
        path: Path to a YAML config file, or ``None`` for pure defaults.

    Raises:
        FileNotFoundError: If ``path`` is given but does not exist.
        ValueError: If the YAML root is not a mapping.
    """
    cfg = Config()
    if not path:
        return cfg

    text = Path(path).read_text(encoding="utf-8")
    data = yaml.safe_load(text) or {}
    if not isinstance(data, dict):
        raise ValueError(f"config file {path} must contain a YAML mapping")

    if "roots" in data:
        cfg.roots = [str(x) for x in data["roots"]]
    if "exclude_dirs" in data:
        cfg.exclude_dirs = [str(x) for x in data["exclude_dirs"]]
    if "exclude_patterns" in data:
        cfg.exclude_patterns = [str(x) for x in data["exclude_patterns"]]
    if "thresholds" in data:
        cfg.thresholds = [int(x) for x in data["thresholds"]]
    if "top_functions" in data:
        cfg.top_functions = int(data["top_functions"])
    if "top_packages" in data:
        cfg.top_packages = int(data["top_packages"])
    if "top_files" in data:
        cfg.top_files = int(data["top_files"])

    return cfg
