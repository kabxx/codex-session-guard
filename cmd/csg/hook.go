package main

import (
	"encoding/json"
	"io"
	"os"
	"time"
)

type HookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
	Source         string `json:"source"`
	Reason         string `json:"reason"`
}

func hookMain(reader io.Reader) int {
	runID := os.Getenv(runIDEnv)
	if !validRunID(runID) {
		return 0
	}
	data, err := io.ReadAll(io.LimitReader(reader, 1<<20))
	if err != nil {
		return 0
	}
	var input HookInput
	if json.Unmarshal(data, &input) != nil {
		return 0
	}
	switch input.HookEventName {
	case "SessionStart":
		if input.SessionID == "" || bindSession(runID, input) != nil {
			return 0
		}
		removeOlderDeadRecords(runID, input.SessionID)
	case "SessionEnd":
		_ = updateRun(runID, func(record *RunRecord) {
			record.SessionEndedAt = time.Now().UTC()
			record.SessionEndReason = input.Reason
		})
	}
	return 0
}

func bindSession(runID string, input HookInput) error {
	ancestorPID, ancestorStart, ancestorFound := nearestCodexAncestor()
	return updateRun(runID, func(record *RunRecord) {
		if record.CodexPID == 0 && ancestorFound {
			record.CodexPID = ancestorPID
			record.CodexStartTime = ancestorStart
		}
		record.SessionID = input.SessionID
		record.TranscriptPath = input.TranscriptPath
		record.SessionSource = input.Source
		if input.CWD != "" {
			record.CWD = input.CWD
		}
	})
}

func removeOlderDeadRecords(currentRunID, sessionID string) {
	records, _ := readAllRuns()
	for _, record := range records {
		if record.RunID == currentRunID || record.SessionID != sessionID || recordIsAlive(record) {
			continue
		}
		_ = removeRun(record.RunID)
	}
}
