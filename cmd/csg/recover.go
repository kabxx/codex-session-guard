package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func listSessionsMain() int {
	sessions, err := trackedSessions()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed to read tracked sessions:", err)
		return 1
	}
	printTrackedSessions(sessions)
	return 0
}

func resumeMain(args []string) int {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Println("Usage: csg resume <session-id>")
		return 0
	}
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: csg resume <session-id>")
		return 2
	}
	return recoverMain(args[0])
}

func deleteMain(args []string) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Println("Usage: csg delete <session-id>")
		return 0
	}
	if len(args) == 1 && args[0] == "--all" {
		fmt.Fprintln(os.Stderr, "csg delete does not support --all; provide one session ID")
		return 2
	}
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: csg delete <session-id>")
		return 2
	}
	return deleteRecoveryMain(args[0])
}

func deleteRecoveryMain(sessionID string) int {
	records, err := readAllRuns()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed to read tracked sessions:", err)
		return 1
	}

	matches := make([]RunRecord, 0)
	for _, record := range records {
		if record.SessionID == sessionID {
			matches = append(matches, record)
		}
	}
	if len(matches) == 0 {
		fmt.Fprintln(os.Stderr, "Crashed session not found:", sessionID)
		return 2
	}
	for _, record := range matches {
		if recordIsAlive(record) {
			fmt.Fprintln(os.Stderr, "The session is still running or its state cannot be verified; its record cannot be deleted:", sessionID)
			return 1
		}
		if recordHasActiveRecoveryClaim(record) {
			fmt.Fprintln(os.Stderr, "The session is being resumed in another terminal; its record cannot be deleted:", sessionID)
			return 1
		}
	}

	claimed := make([]string, 0, len(matches))
	releaseClaims := func() {
		for _, runID := range claimed {
			_ = releaseRunClaim(runID)
		}
	}
	for _, record := range matches {
		if err := claimRunForDelete(record.RunID, sessionID); err != nil {
			releaseClaims()
			switch {
			case errors.Is(err, errRecoveryInProgress):
				fmt.Fprintln(os.Stderr, "The session is being resumed in another terminal; its record cannot be deleted:", sessionID)
			case errors.Is(err, errDeleteSessionAlive):
				fmt.Fprintln(os.Stderr, "The session is still running or its state cannot be verified; its record cannot be deleted:", sessionID)
			case errors.Is(err, errDeleteSessionChanged):
				fmt.Fprintln(os.Stderr, "The session record changed; run csg list and try again:", sessionID)
			default:
				fmt.Fprintln(os.Stderr, "Failed to claim the record for deletion:", err)
			}
			return 1
		}
		claimed = append(claimed, record.RunID)
	}
	for _, runID := range claimed {
		if err := removeClaimedRun(runID, sessionID); err != nil {
			releaseClaims()
			if errors.Is(err, errDeleteSessionChanged) || errors.Is(err, errDeleteClaimLost) {
				fmt.Fprintln(os.Stderr, "The session record changed; run csg list and try again:", sessionID)
			} else {
				fmt.Fprintln(os.Stderr, "Failed to delete the session record:", err)
			}
			return 1
		}
	}

	fmt.Println("Deleted session record:", sessionID)
	return 0
}

func recoverMain(sessionID string) int {
	records, err := recoveryCandidates()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed to read crashed sessions:", err)
		return 1
	}

	record, ok := findRecordBySessionID(records, sessionID)
	if !ok {
		fmt.Fprintln(os.Stderr, "Crashed session not found:", sessionID)
		return 2
	}
	settings, err := loadSettings()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed to load Codex Session Guard settings:", err)
		return 1
	}
	target, err := targetForRecord(record, settings)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed to resume with the original launcher:", err)
		fmt.Fprintln(os.Stderr, "The crashed-session record was preserved.")
		return 1
	}
	if err := claimRun(record.RunID); err != nil {
		if errors.Is(err, errRecoveryInProgress) {
			fmt.Fprintln(os.Stderr, "The session is already being resumed in another terminal.")
		} else {
			fmt.Fprintln(os.Stderr, "Failed to claim the crashed-session record:", err)
		}
		return 1
	}
	fmt.Printf("Resuming %s with %s\n", record.SessionID, launcherLabel(record))
	workingDir := record.CWD
	if info, err := os.Stat(workingDir); err != nil || !info.IsDir() {
		fmt.Fprintln(os.Stderr, "The original working directory no longer exists; resuming from the current directory:", workingDir)
		workingDir = ""
	}
	code := runGuarded(target, []string{"resume", record.SessionID}, workingDir, settings)
	if err := releaseRunClaim(record.RunID); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "Failed to release the recovery claim:", err)
	}
	return code
}

type trackedStatus string

const (
	statusStarting trackedStatus = "STARTING"
	statusRunning  trackedStatus = "RUNNING"
	statusUnknown  trackedStatus = "UNKNOWN"
	statusCrashed  trackedStatus = "CRASHED"
)

type trackedSession struct {
	Record RunRecord
	Status trackedStatus
}

func printTrackedSessions(sessions []trackedSession) {
	if len(sessions) == 0 {
		fmt.Println("No sessions are currently tracked.")
		return
	}
	active, unknown, crashed := 0, 0, 0
	for _, session := range sessions {
		switch session.Status {
		case statusCrashed:
			crashed++
		case statusUnknown:
			unknown++
		default:
			active++
		}
	}
	fmt.Printf("Tracked sessions: %d active, %d unknown, %d crashed\n\n", active, unknown, crashed)
	for _, session := range sessions {
		record := session.Record
		when := record.StartedAt.Local().Format("2006-01-02 15:04:05")
		sessionID := record.SessionID
		if sessionID == "" {
			sessionID = "(pending)"
		}
		fmt.Printf("%-8s %s\n", session.Status, sessionID)
		fmt.Printf("  Started:   %s\n  Launcher:  %s\n  Directory: %s\n", when, launcherLabel(record), record.CWD)
		if session.Status == statusCrashed && record.ExitCode != 0 {
			fmt.Printf("  Exit code: %d\n", record.ExitCode)
		}
		fmt.Println()
	}
}

func findRecordBySessionID(records []RunRecord, sessionID string) (RunRecord, bool) {
	for _, record := range records {
		if record.SessionID == sessionID {
			return record, true
		}
	}
	return RunRecord{}, false
}

func recoverableRuns() ([]RunRecord, error) {
	return recoveryCandidates()
}

func recoveryCandidates() ([]RunRecord, error) {
	sessions, err := trackedSessions()
	if err != nil {
		return nil, err
	}
	result := make([]RunRecord, 0)
	for _, session := range sessions {
		if session.Status == statusCrashed {
			result = append(result, session.Record)
		}
	}
	return result, nil
}

func trackedSessions() ([]trackedSession, error) {
	records, err := readAllRuns()
	if err != nil {
		return nil, err
	}
	sort.Slice(records, func(i, j int) bool { return records[i].StartedAt.After(records[j].StartedAt) })
	seen := make(map[string]bool)
	result := make([]trackedSession, 0, len(records))
	for _, record := range records {
		status := statusForRecord(record)
		if status == statusCrashed && record.SessionID == "" {
			if err := removeRun(record.RunID); err != nil {
				return nil, fmt.Errorf("clean up unbound run record %s: %w", record.RunID, err)
			}
			continue
		}
		if record.SessionID != "" {
			if seen[record.SessionID] {
				continue
			}
			seen[record.SessionID] = true
		}
		result = append(result, trackedSession{Record: record, Status: status})
	}
	return result, nil
}

func statusForRecord(record RunRecord) trackedStatus {
	switch recordProcessLiveness(record) {
	case processAlive:
		if record.SessionID == "" {
			return statusStarting
		}
		return statusRunning
	case processUnknown:
		return statusUnknown
	default:
		switch processLiveness(record.RecoveryPID, record.RecoveryStartTime) {
		case processAlive:
			return statusRunning
		case processUnknown:
			return statusUnknown
		default:
			return statusCrashed
		}
	}
}

func readAllRuns() ([]RunRecord, error) {
	dir, err := runsPath()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]RunRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".json" {
			continue
		}
		var record RunRecord
		if readJSON(filepath.Join(dir, entry.Name()), &record) == nil && validRunID(record.RunID) && (record.Version == 1 || record.Version == 2) {
			result = append(result, record)
		}
	}
	return result, nil
}

func recordIsAlive(record RunRecord) bool {
	return recordProcessLiveness(record) != processDead
}

func recordProcessLiveness(record RunRecord) processState {
	states := []processState{
		processLiveness(record.CodexPID, record.CodexStartTime),
		processLiveness(record.WrapperPID, record.WrapperStartTime),
	}
	result := processDead
	for _, state := range states {
		if state == processAlive {
			return processAlive
		}
		if state == processUnknown {
			result = processUnknown
		}
	}
	return result
}
