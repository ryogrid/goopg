#!/usr/bin/env python3
"""
Extract deferred tasks from completed milestones that haven't been addressed.
"""

import re
from pathlib import Path
from typing import List, Dict, Tuple, Set
from dataclasses import dataclass

@dataclass
class DeferredTask:
    """Represents a deferred task."""
    milestone_num: str  # e.g., "M0003"
    milestone_title: str
    task_text: str
    deferral_context: str
    line_num: int

# Deferral patterns
DEFERRAL_PATTERNS = [
    r'\bdeferred\b',
    r'\bremain\s+deferred\b',
    r'\bstay\s+deferred\b',
    r'\bstays\s+deferred\b',
    r'\bout\s+of\s+scope\b',
    r'\bpostponed\b',
    r'\bfuture\s+work\b',
    r'\bfuture\s+milestone\b',
    r'\blater\s+milestone\b',
    r'\bnext-loop\s+work\b',
]

def normalize_milestone_num(raw_num: str) -> str:
    """Convert milestone number to M0000 format."""
    num = int(raw_num)
    return f"M{num:04d}"

def parse_milestone_header(line: str) -> Tuple[str, str] | None:
    """Parse milestone header, return (number, title) or None."""
    # Match: ## Milestone N — Title or ## Milestone 0NNN — Title
    match = re.match(r'^##\s+Milestone\s+(\d+)\s+—\s+(.+)$', line.strip())
    if match:
        num = normalize_milestone_num(match.group(1))
        title = match.group(2)
        return (num, title)
    return None

def has_deferral(text: str) -> Tuple[bool, str]:
    """Check if text contains deferral pattern, return (found, context)."""
    text_lower = text.lower()
    for pattern in DEFERRAL_PATTERNS:
        match = re.search(pattern, text_lower)
        if match:
            # Extract context: sentence containing the match
            start = max(0, match.start() - 100)
            end = min(len(text), match.end() + 100)
            context = text[start:end].strip()
            return (True, context)
    return (False, "")

def parse_completed_file(filepath: Path) -> List[DeferredTask]:
    """Parse a completed milestone file and extract deferred tasks."""
    deferred_tasks = []
    current_milestone_num = None
    current_milestone_title = None
    current_task_lines = []
    current_task_start_line = 0
    in_task = False

    lines = filepath.read_text().splitlines()

    for i, line in enumerate(lines, 1):
        # Check for milestone header
        milestone_info = parse_milestone_header(line)
        if milestone_info:
            # Process any pending task
            if in_task and current_task_lines:
                task_text = '\n'.join(current_task_lines)
                has_def, context = has_deferral(task_text)
                if has_def:
                    deferred_tasks.append(DeferredTask(
                        milestone_num=current_milestone_num,
                        milestone_title=current_milestone_title,
                        task_text=task_text,
                        deferral_context=context,
                        line_num=current_task_start_line
                    ))

            # Update milestone context
            current_milestone_num, current_milestone_title = milestone_info
            in_task = False
            current_task_lines = []
            continue

        # Check for task start
        if re.match(r'^- \[[x ]\]', line):
            # Process previous task if any
            if in_task and current_task_lines:
                task_text = '\n'.join(current_task_lines)
                has_def, context = has_deferral(task_text)
                if has_def:
                    deferred_tasks.append(DeferredTask(
                        milestone_num=current_milestone_num,
                        milestone_title=current_milestone_title,
                        task_text=task_text,
                        deferral_context=context,
                        line_num=current_task_start_line
                    ))

            # Start new task
            in_task = True
            current_task_lines = [line]
            current_task_start_line = i
        elif in_task:
            # Continuation line (starts with spaces or is blank)
            if line.strip() == '' or line.startswith((' ', '\t')):
                current_task_lines.append(line)
            else:
                # End of task, this is a new section
                if current_task_lines:
                    task_text = '\n'.join(current_task_lines)
                    has_def, context = has_deferral(task_text)
                    if has_def:
                        deferred_tasks.append(DeferredTask(
                            milestone_num=current_milestone_num,
                            milestone_title=current_milestone_title,
                            task_text=task_text,
                            deferral_context=context,
                            line_num=current_task_start_line
                        ))
                in_task = False
                current_task_lines = []

    # Process last task if any
    if in_task and current_task_lines:
        task_text = '\n'.join(current_task_lines)
        has_def, context = has_deferral(task_text)
        if has_def:
            deferred_tasks.append(DeferredTask(
                milestone_num=current_milestone_num,
                milestone_title=current_milestone_title,
                task_text=task_text,
                deferral_context=context,
                line_num=current_task_start_line
            ))

    return deferred_tasks

def extract_keywords(text: str) -> Set[str]:
    """Extract significant keywords from text for matching."""
    # Remove common words and extract technical terms
    words = re.findall(r'\b[A-Za-z_][A-Za-z0-9_]{2,}\b', text)
    stopwords = {'the', 'and', 'are', 'for', 'with', 'from', 'this', 'that', 'have', 'been', 'will', 'can', 'may'}
    keywords = {w.lower() for w in words if w.lower() not in stopwords}
    return keywords

def is_completed_in_fix_plan(task: DeferredTask, fix_plan_text: str) -> bool:
    """Check if task appears to be completed in fix_plan.md."""
    # Be conservative: only exclude if there's STRONG evidence of completion
    # Default to including deferred tasks in output

    # If task is explicitly marked DEFERRED, it's definitely not completed
    if '(DEFERRED)' in task.task_text:
        return False

    # Extract keywords from task (technical terms only)
    keywords = extract_keywords(task.task_text)
    if len(keywords) < 2:
        return False  # Not enough context to verify

    # Extract milestone target if mentioned
    milestone_target_match = re.search(r'\bdeferred\s+to\s+(?:milestone\s+)?(\d+|M0\d+)\b', task.task_text, re.IGNORECASE)
    if milestone_target_match:
        target = milestone_target_match.group(1)
        if not target.startswith('M'):
            target = normalize_milestone_num(target)

        # Only exclude if target milestone >= M0054 (active milestones)
        target_num = int(target[1:])
        if target_num < 54:
            # Deferred to earlier milestone - likely not done yet
            return False

        # Check if target milestone exists in fix_plan and has strong keyword overlap
        if target in fix_plan_text:
            target_pattern = f'## {target} —'
            target_match = re.search(target_pattern, fix_plan_text)
            if target_match:
                # Search next 10000 chars after milestone header
                section = fix_plan_text[target_match.start():target_match.start() + 10000]
                section_lower = section.lower()

                # Need high keyword density to consider it completed
                keyword_matches = sum(1 for kw in keywords if kw in section_lower)
                keyword_density = keyword_matches / len(keywords) if keywords else 0

                # Require at least 70% keyword match AND absolute count >= 5
                if keyword_density >= 0.7 and keyword_matches >= 5:
                    return True

    # If no strong evidence, keep the task (it's still deferred)
    return False

def generate_output(deferred_tasks: List[DeferredTask], output_path: Path):
    """Generate the maybe_not_completed_tasks.md file."""
    # Sort by milestone number
    deferred_tasks.sort(key=lambda t: t.milestone_num)

    # Group by milestone
    milestones = {}
    for task in deferred_tasks:
        key = (task.milestone_num, task.milestone_title)
        if key not in milestones:
            milestones[key] = []
        milestones[key].append(task)

    # Generate output
    lines = [
        "# Potentially Incomplete Deferred Tasks",
        "",
        "This document contains tasks deferred from completed milestones (M0000-M0053)",
        "that appear not to have been addressed in active or later milestones.",
        "",
        "**Generated:** 2026-05-07",
        "",
        "---",
        ""
    ]

    for (milestone_num, milestone_title), tasks in sorted(milestones.items()):
        # Extract numeric part for header
        num_str = milestone_num[1:]  # Remove 'M' prefix
        lines.append(f"## Milestone {num_str} — {milestone_title}")
        lines.append("")

        for task in tasks:
            # Remove checkbox and preserve rest
            task_lines = task.task_text.splitlines()
            if task_lines:
                # First line: remove checkbox
                first_line = task_lines[0]
                first_line = re.sub(r'^- \[[x ]\]\s*', '- ', first_line)
                lines.append(first_line)

                # Rest of lines: remove checkboxes from nested tasks too
                for line in task_lines[1:]:
                    # Remove checkboxes from indented sub-tasks
                    line = re.sub(r'^(\s+)- \[[x ]\]\s*', r'\1- ', line)
                    lines.append(line)

                lines.append("")  # Blank line between tasks

        lines.append("")  # Extra blank line between milestones

    output_path.write_text('\n'.join(lines))

def main():
    base_dir = Path("/home/ryo/work/goopg/goopg")
    completed_dir = base_dir / "completed_milestones"

    # Parse all completed files
    print("Parsing completed milestone files...")
    all_deferred = []

    for i in range(1, 4):
        filepath = completed_dir / f"completed_fix_plan_{i:03d}.md"
        print(f"  Reading {filepath.name}...")
        deferred = parse_completed_file(filepath)
        print(f"    Found {len(deferred)} deferred tasks")
        all_deferred.extend(deferred)

    print(f"\nTotal deferred tasks found: {len(all_deferred)}")

    # Read fix_plan.md for verification
    print("\nReading fix_plan.md for verification...")
    fix_plan_path = base_dir / ".ralph" / "fix_plan.md"
    fix_plan_text = fix_plan_path.read_text()

    # Filter out tasks that appear to be completed
    print("\nVerifying tasks against fix_plan.md...")
    unverified_tasks = []
    completed_count = 0

    for task in all_deferred:
        if not is_completed_in_fix_plan(task, fix_plan_text):
            unverified_tasks.append(task)
        else:
            completed_count += 1
            print(f"  Excluded (appears completed): {task.milestone_num} - {task.deferral_context[:60]}...")

    print(f"\nTasks remaining after verification: {len(unverified_tasks)}")
    print(f"Tasks excluded as completed: {completed_count}")

    # Generate output
    output_path = base_dir / "maybe_not_completed_tasks.md"
    print(f"\nGenerating output file: {output_path}")
    generate_output(unverified_tasks, output_path)

    print(f"\nDone! Output written to {output_path}")
    print(f"Total tasks in output: {len(unverified_tasks)}")

if __name__ == "__main__":
    main()
