package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

type jsonObject map[string]any

type statusFile struct {
	Timestamp          string `json:"timestamp"`
	LoopCount          int    `json:"loop_count"`
	CallsMadeThisHour  int    `json:"calls_made_this_hour"`
	MaxCallsPerHour    int    `json:"max_calls_per_hour"`
	TokensUsedThisHour int    `json:"tokens_used_this_hour"`
	MaxTokensPerHour   int    `json:"max_tokens_per_hour"`
	LastAction         string `json:"last_action"`
	Status             string `json:"status"`
	ExitReason         string `json:"exit_reason"`
}

type progressFile struct {
	Status    string `json:"status"`
	Timestamp string `json:"timestamp"`
}

func main() {
	var (
		statusPath   = flag.String("status", ".ralph/status.json", "path to Ralph status.json")
		progressPath = flag.String("progress", ".ralph/progress.json", "path to Ralph progress.json")
		maxSkew      = flag.Duration("max-skew", 2*time.Minute, "max tolerated timestamp skew before reporting inconsistency")
		fix          = flag.Bool("fix", false, "attempt safe in-place repair when known stale-state patterns are detected")
	)
	flag.Parse()

	status, statusRaw, err := loadStatus(*statusPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "validate-ralph-state: read status: %v\n", err)
		os.Exit(2)
	}
	progress, progressRaw, err := loadProgress(*progressPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "validate-ralph-state: read progress: %v\n", err)
		os.Exit(2)
	}

	issues := validate(status, progress, *maxSkew)
	if len(issues) == 0 {
		fmt.Printf("OK: status/progress are consistent (status=%q, progress=%q)\n", status.Status, progress.Status)
		return
	}

	if *fix {
		repairedStatus, repairedProgress, repairs := autoRepair(status, progress, *maxSkew)
		if len(repairs) == 0 {
			fmt.Printf("INCONSISTENT: %d issue(s) found; no safe auto-repair rule matched\n", len(issues))
			printSummary(status, progress, issues)
			os.Exit(1)
		}

		postIssues := validate(repairedStatus, repairedProgress, *maxSkew)
		if len(postIssues) > 0 {
			fmt.Printf("INCONSISTENT: auto-repair applied in-memory but unresolved issues remain\n")
			for i, r := range repairs {
				fmt.Printf("repair %d. %s\n", i+1, r)
			}
			printSummary(repairedStatus, repairedProgress, postIssues)
			os.Exit(1)
		}

		if repairedStatus != status {
			applyStatusToRaw(statusRaw, repairedStatus)
			if err := writeJSON(*statusPath, statusRaw); err != nil {
				fmt.Fprintf(os.Stderr, "validate-ralph-state: write status: %v\n", err)
				os.Exit(2)
			}
		}
		if repairedProgress != progress {
			applyProgressToRaw(progressRaw, repairedProgress)
			if err := writeJSON(*progressPath, progressRaw); err != nil {
				fmt.Fprintf(os.Stderr, "validate-ralph-state: write progress: %v\n", err)
				os.Exit(2)
			}
		}

		fmt.Printf("REPAIRED: applied %d fix(es)\n", len(repairs))
		for i, r := range repairs {
			fmt.Printf("%d. %s\n", i+1, r)
		}
		fmt.Printf("OK: status/progress are consistent after repair (status=%q, progress=%q)\n", repairedStatus.Status, repairedProgress.Status)
		return
	}

	fmt.Printf("INCONSISTENT: %d issue(s) found\n", len(issues))
	printSummary(status, progress, issues)
	os.Exit(1)
}

func printSummary(status statusFile, progress progressFile, issues []string) {
	fmt.Printf("status:   status=%q last_action=%q loop_count=%d timestamp=%q\n", status.Status, status.LastAction, status.LoopCount, status.Timestamp)
	fmt.Printf("progress: status=%q timestamp=%q\n", progress.Status, progress.Timestamp)
	for i, issue := range issues {
		fmt.Printf("%d. %s\n", i+1, issue)
	}
}

func autoRepair(status statusFile, progress progressFile, maxSkew time.Duration) (statusFile, progressFile, []string) {
	repairs := make([]string, 0)
	repairedStatus := status
	repairedProgress := progress

	statusTS, statusErr := parseTimestamp(status.Timestamp)
	progressTS, progressErr := parseTimestamp(progress.Timestamp)
	if statusErr == nil && progressErr == nil {
		isStatusRunning := strings.EqualFold(status.Status, "running")
		isProgressCompleted := strings.EqualFold(progress.Status, "completed")
		isExecuting := strings.EqualFold(status.LastAction, "executing")

		// Safe rule: stale "running/executing" status with newer completed progress.
		if isStatusRunning && isProgressCompleted && isExecuting && progressTS.After(statusTS.Add(maxSkew)) {
			repairedStatus.Status = "completed"
			repairedStatus.LastAction = "idle"
			repairedStatus.Timestamp = progressTS.Format(time.RFC3339)
			repairs = append(repairs,
				fmt.Sprintf("status marked completed/idle because progress completed is newer by > %s", maxSkew))
		}
	}

	return repairedStatus, repairedProgress, repairs
}

func validate(status statusFile, progress progressFile, maxSkew time.Duration) []string {
	issues := make([]string, 0)

	if strings.TrimSpace(status.Status) == "" {
		issues = append(issues, "status.status is empty")
	}
	if strings.TrimSpace(progress.Status) == "" {
		issues = append(issues, "progress.status is empty")
	}
	if status.LoopCount < 0 {
		issues = append(issues, fmt.Sprintf("status.loop_count is negative: %d", status.LoopCount))
	}
	if status.CallsMadeThisHour < 0 {
		issues = append(issues, fmt.Sprintf("status.calls_made_this_hour is negative: %d", status.CallsMadeThisHour))
	}
	if status.TokensUsedThisHour < 0 {
		issues = append(issues, fmt.Sprintf("status.tokens_used_this_hour is negative: %d", status.TokensUsedThisHour))
	}

	if status.MaxCallsPerHour > 0 && status.CallsMadeThisHour > status.MaxCallsPerHour {
		issues = append(issues,
			fmt.Sprintf("calls_made_this_hour (%d) exceeds max_calls_per_hour (%d)",
				status.CallsMadeThisHour, status.MaxCallsPerHour))
	}
	if status.MaxTokensPerHour > 0 && status.TokensUsedThisHour > status.MaxTokensPerHour {
		issues = append(issues,
			fmt.Sprintf("tokens_used_this_hour (%d) exceeds max_tokens_per_hour (%d)",
				status.TokensUsedThisHour, status.MaxTokensPerHour))
	}

	isStatusRunning := strings.EqualFold(status.Status, "running")
	isProgressRunning := strings.EqualFold(progress.Status, "running") || strings.EqualFold(progress.Status, "in_progress")
	isProgressCompleted := strings.EqualFold(progress.Status, "completed")

	if isStatusRunning && isProgressCompleted {
		issues = append(issues, "status.status is running while progress.status is completed")
	}
	if !isStatusRunning && isProgressRunning {
		issues = append(issues, "progress.status indicates running/in_progress while status.status is not running")
	}

	isExecuting := strings.EqualFold(status.LastAction, "executing")
	if isExecuting && !isStatusRunning {
		issues = append(issues, "status.last_action is executing but status.status is not running")
	}
	if isExecuting && isProgressCompleted {
		issues = append(issues, "status.last_action is executing but progress.status is completed")
	}

	statusTS, statusErr := parseTimestamp(status.Timestamp)
	progressTS, progressErr := parseTimestamp(progress.Timestamp)
	if statusErr != nil {
		issues = append(issues, fmt.Sprintf("status.timestamp parse error: %v", statusErr))
	}
	if progressErr != nil {
		issues = append(issues, fmt.Sprintf("progress.timestamp parse error: %v", progressErr))
	}

	if statusErr == nil && progressErr == nil {
		if isStatusRunning && progressTS.After(statusTS.Add(maxSkew)) {
			issues = append(issues,
				fmt.Sprintf("progress.timestamp (%s) is newer than status.timestamp (%s) by more than max-skew (%s) while status is running",
					progressTS.Format(time.RFC3339), statusTS.Format(time.RFC3339), maxSkew))
		}
		if isProgressCompleted && statusTS.After(progressTS.Add(maxSkew)) {
			issues = append(issues,
				fmt.Sprintf("status.timestamp (%s) is newer than progress.timestamp (%s) by more than max-skew (%s) while progress is completed",
					statusTS.Format(time.RFC3339), progressTS.Format(time.RFC3339), maxSkew))
		}
	}

	return issues
}

func parseTimestamp(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, errors.New("empty timestamp")
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
	}
	for _, layout := range layouts {
		t, err := time.Parse(layout, raw)
		if err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported format: %q", raw)
}

func loadStatus(path string) (statusFile, jsonObject, error) {
	var v statusFile
	var raw jsonObject
	b, err := os.ReadFile(path)
	if err != nil {
		return v, nil, err
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return v, nil, err
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return v, nil, err
	}
	return v, raw, nil
}

func loadProgress(path string) (progressFile, jsonObject, error) {
	var v progressFile
	var raw jsonObject
	b, err := os.ReadFile(path)
	if err != nil {
		return v, nil, err
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return v, nil, err
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return v, nil, err
	}
	return v, raw, nil
}

func applyStatusToRaw(raw jsonObject, status statusFile) {
	raw["timestamp"] = status.Timestamp
	raw["loop_count"] = status.LoopCount
	raw["calls_made_this_hour"] = status.CallsMadeThisHour
	raw["max_calls_per_hour"] = status.MaxCallsPerHour
	raw["tokens_used_this_hour"] = status.TokensUsedThisHour
	raw["max_tokens_per_hour"] = status.MaxTokensPerHour
	raw["last_action"] = status.LastAction
	raw["status"] = status.Status
	raw["exit_reason"] = status.ExitReason
}

func applyProgressToRaw(raw jsonObject, progress progressFile) {
	raw["status"] = progress.Status
	raw["timestamp"] = progress.Timestamp
}

func writeJSON(path string, raw jsonObject) error {
	b, err := json.MarshalIndent(raw, "", "    ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}
