#!/usr/bin/env python3

import argparse
import csv
import json
import shutil
import subprocess
import tempfile
from collections import defaultdict
from datetime import date, timedelta
from pathlib import Path

import pandas as pd
import matplotlib.pyplot as plt


CACHE_DIR = Path(".git/loc_cache")


def run(cmd):
    return subprocess.check_output(
        cmd,
        text=True,
        stderr=subprocess.DEVNULL,
    ).strip()


def ensure_requirements():
    for cmd in ["git", "cloc"]:
        if shutil.which(cmd) is None:
            raise RuntimeError(f"{cmd} not found")


def get_commit_before(target_date):
    try:
        commit = run([
            "git",
            "rev-list",
            "-n",
            "1",
            f"--before={target_date.isoformat()} 23:59:59",
            "HEAD",
        ])
        return commit or None
    except subprocess.CalledProcessError:
        return None


def cache_path(commit, depth):
    return CACHE_DIR / f"{commit}_d{depth}.json"


def load_cache(commit, depth):
    path = cache_path(commit, depth)

    if not path.exists():
        return None

    with open(path, encoding="utf-8") as f:
        return json.load(f)


def save_cache(commit, depth, data):
    CACHE_DIR.mkdir(parents=True, exist_ok=True)

    with open(
        cache_path(commit, depth),
        "w",
        encoding="utf-8"
    ) as f:
        json.dump(data, f)


def export_commit(commit, destination):
    archive_path = Path(destination) / "repo.tar"

    with open(archive_path, "wb") as f:
        subprocess.check_call(
            ["git", "archive", commit],
            stdout=f,
        )

    subprocess.check_call(
        [
            "tar",
            "-xf",
            str(archive_path),
            "-C",
            destination,
        ]
    )


def normalize_group(relpath: Path, depth: int):
    parent = relpath.parent

    if str(parent) == ".":
        return "."

    parts = parent.parts

    if len(parts) <= depth:
        return "/".join(parts)

    return "/".join(parts[:depth])


def run_cloc(root):
    output = subprocess.check_output(
        [
            "cloc",
            str(root),
            "--include-ext=go",
            "--by-file",
            "--json",
            "--quiet",
        ],
        text=True,
    )

    return json.loads(output)


def analyze_commit(commit, depth):
    cached = load_cache(commit, depth)

    if cached:
        return cached

    with tempfile.TemporaryDirectory() as tmpdir:

        export_commit(commit, tmpdir)

        cloc_data = run_cloc(tmpdir)

        directory_loc = defaultdict(int)

        for filename, stats in cloc_data.items():

            if filename in ("header", "SUM"):
                continue

            if not filename.endswith(".go"):
                continue

            code = stats.get("code", 0)

            try:
                relpath = Path(filename).relative_to(tmpdir)
            except Exception:
                relpath = Path(filename)

            group = normalize_group(
                relpath,
                depth
            )

            directory_loc[group] += code

        result = {
            "total": sum(directory_loc.values()),
            "directories": dict(directory_loc),
        }

        save_cache(
            commit,
            depth,
            result,
        )

        return result


def generate_csv(rows, output_file):
    with open(
        output_file,
        "w",
        newline="",
        encoding="utf-8",
    ) as f:

        writer = csv.writer(f)

        writer.writerow([
            "date",
            "directory",
            "loc",
        ])

        writer.writerows(rows)


def generate_graph(
    csv_file,
    png_file,
    top_n,
):
    df = pd.read_csv(csv_file)

    if df.empty:
        return

    pivot = (
        df.pivot(
            index="date",
            columns="directory",
            values="loc",
        )
        .fillna(0)
        .sort_index()
    )

    plt.figure(figsize=(16, 9))

    if "TOTAL" in pivot.columns:
        plt.plot(
            pivot.index,
            pivot["TOTAL"],
            linewidth=3,
            label="TOTAL",
        )

    candidates = [
        c
        for c in pivot.columns
        if c != "TOTAL"
    ]

    if candidates:

        ranking = (
            pivot[candidates]
            .max()
            .sort_values(
                ascending=False
            )
            .head(top_n)
            .index
        )

        for directory in ranking:

            plt.plot(
                pivot.index,
                pivot[directory],
                label=directory,
            )

    plt.title(
        "Go LOC History"
    )

    plt.xlabel("Date")
    plt.ylabel("LOC")

    plt.xticks(rotation=45)

    plt.legend()

    plt.tight_layout()

    plt.savefig(
        png_file,
        dpi=150,
    )

    plt.close()


def main():

    ensure_requirements()

    parser = argparse.ArgumentParser()

    parser.add_argument(
        "--weeks",
        type=int,
        default=52,
        help="Number of weeks"
    )

    parser.add_argument(
        "--depth",
        type=int,
        default=1,
        help="Directory grouping depth"
    )

    parser.add_argument(
        "--csv",
        default="weekly_loc.csv"
    )

    parser.add_argument(
        "--png",
        default="weekly_loc.png"
    )

    parser.add_argument(
        "--top-n",
        type=int,
        default=10
    )

    args = parser.parse_args()

    rows = []

    today = date.today()

    analyzed = {}

    for week in reversed(
        range(args.weeks)
    ):

        target_day = (
            today
            - timedelta(days=week * 7)
        )

        commit = get_commit_before(
            target_day
        )

        if commit is None:
            continue

        if commit not in analyzed:
            analyzed[commit] = (
                analyze_commit(
                    commit,
                    args.depth,
                )
            )

        result = analyzed[commit]

        rows.append([
            target_day.isoformat(),
            "TOTAL",
            result["total"],
        ])

        for directory, loc in sorted(
            result["directories"].items()
        ):
            rows.append([
                target_day.isoformat(),
                directory,
                loc,
            ])

        print(
            target_day,
            commit[:8],
            result["total"],
        )

    generate_csv(
        rows,
        args.csv,
    )

    generate_graph(
        args.csv,
        args.png,
        args.top_n,
    )

    print()
    print(
        f"CSV generated: {args.csv}"
    )
    print(
        f"PNG generated: {args.png}"
    )


if __name__ == "__main__":
    main()

