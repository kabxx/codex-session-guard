package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Settings struct {
	Version             int       `json:"version"`
	InstallDir          string    `json:"install_dir"`
	RealCodexPath       string    `json:"real_codex_path"`
	CodexVersion        string    `json:"codex_version"`
	CodexHome           string    `json:"codex_home"`
	ManagedPackageRoot  string    `json:"managed_package_root,omitempty"`
	ManagedBy           string    `json:"managed_by,omitempty"`
	BinaryHash          string    `json:"binary_hash"`
	HookKey             string    `json:"hook_key"`
	HookHash            string    `json:"hook_hash"`
	SessionEndHookKey   string    `json:"session_end_hook_key,omitempty"`
	SessionEndHookHash  string    `json:"session_end_hook_hash,omitempty"`
	HooksFileWasCreated bool      `json:"hooks_file_was_created"`
	InstalledAt         time.Time `json:"installed_at"`
}

type RunRecord struct {
	Version           int             `json:"version"`
	RunID             string          `json:"run_id"`
	StartedAt         time.Time       `json:"started_at"`
	EndedAt           time.Time       `json:"ended_at,omitempty"`
	CWD               string          `json:"cwd"`
	WrapperPID        int             `json:"wrapper_pid"`
	WrapperStartTime  uint64          `json:"wrapper_start_time"`
	CodexPID          int             `json:"codex_pid"`
	CodexStartTime    uint64          `json:"codex_start_time"`
	SessionID         string          `json:"session_id,omitempty"`
	TranscriptPath    string          `json:"transcript_path,omitempty"`
	SessionSource     string          `json:"session_source,omitempty"`
	ExitCode          int             `json:"exit_code,omitempty"`
	ExitError         string          `json:"exit_error,omitempty"`
	RecoveryPID       int             `json:"recovery_pid,omitempty"`
	RecoveryStartTime uint64          `json:"recovery_start_time,omitempty"`
	RecoveryClaimedAt time.Time       `json:"recovery_claimed_at,omitempty"`
	Launcher          *LauncherRecord `json:"launcher,omitempty"`
	SessionEndedAt    time.Time       `json:"session_ended_at,omitempty"`
	SessionEndReason  string          `json:"session_end_reason,omitempty"`
}

type LauncherRecord struct {
	Requested    string `json:"requested"`
	ResolvedPath string `json:"resolved_path"`
	Platform     string `json:"platform"`
}

func guardHome() (string, error) {
	if value := strings.TrimSpace(os.Getenv("CODEX_SESSION_GUARD_HOME")); value != "" {
		return filepath.Abs(value)
	}
	switch runtime.GOOS {
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			return "", errors.New("LOCALAPPDATA is not set")
		}
		return filepath.Join(local, "CodexSessionGuard"), nil
	case "darwin":
		user, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(user, "Library", "Application Support", "CodexSessionGuard"), nil
	default:
		if stateHome := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); stateHome != "" {
			return filepath.Join(stateHome, "codex-session-guard"), nil
		}
		user, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(user, ".local", "state", "codex-session-guard"), nil
	}
}

func codexHome() (string, error) {
	if value := strings.TrimSpace(os.Getenv("CODEX_HOME")); value != "" {
		return filepath.Abs(value)
	}
	user, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(user, ".codex"), nil
}

func settingsPath() (string, error) {
	home, err := guardHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "settings.json"), nil
}

func runsPath() (string, error) {
	home, err := guardHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "runs"), nil
}

func loadSettings() (Settings, error) {
	var settings Settings
	path, err := settingsPath()
	if err != nil {
		return settings, err
	}
	if err := readJSON(path, &settings); err != nil {
		return settings, fmt.Errorf("read settings: %w", err)
	}
	if settings.InstallDir == "" {
		return settings, errors.New("settings are incomplete; run the installer again")
	}
	return settings, nil
}

func newRunID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func validRunID(value string) bool {
	if len(value) != 32 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func runFile(runID string) (string, error) {
	if !validRunID(runID) {
		return "", errors.New("invalid run ID")
	}
	dir, err := runsPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, runID+".json"), nil
}

func saveNewRun(record RunRecord) error {
	if record.Version != 1 && record.Version != 2 {
		return fmt.Errorf("unsupported run-record version: %d", record.Version)
	}
	path, err := runFile(record.RunID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeJSONAtomic(path, record)
}

func loadRun(runID string) (RunRecord, error) {
	var record RunRecord
	path, err := runFile(runID)
	if err != nil {
		return record, err
	}
	if err := readJSON(path, &record); err != nil {
		return record, err
	}
	if record.Version != 1 && record.Version != 2 {
		return record, fmt.Errorf("unsupported run-record version: %d", record.Version)
	}
	return record, nil
}

func updateRun(runID string, mutate func(*RunRecord)) error {
	return mutateRun(runID, func(record *RunRecord) error {
		mutate(record)
		return nil
	})
}

func mutateRun(runID string, mutate func(*RunRecord) error) error {
	path, err := runFile(runID)
	if err != nil {
		return err
	}
	unlock, err := acquireFileLock(path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()

	var record RunRecord
	if err := readJSON(path, &record); err != nil {
		return err
	}
	if err := mutate(&record); err != nil {
		return err
	}
	return writeJSONAtomic(path, record)
}

var errRecoveryInProgress = errors.New("the session is being resumed in another terminal")

var (
	errDeleteSessionChanged = errors.New("the session ID of the record being deleted changed")
	errDeleteSessionAlive   = errors.New("the session being deleted is still running")
	errDeleteClaimLost      = errors.New("the current process no longer owns the deletion claim")
)

func claimRun(runID string) error {
	pid := os.Getpid()
	started, alive := processIdentity(pid)
	if !alive {
		return errors.New("cannot verify the recovery process state")
	}
	return mutateRun(runID, func(record *RunRecord) error {
		if record.SessionID == "" {
			return errors.New("the record has no recoverable session ID")
		}
		if recordHasActiveRecoveryClaim(*record) {
			return errRecoveryInProgress
		}
		record.RecoveryPID = pid
		record.RecoveryStartTime = started
		record.RecoveryClaimedAt = time.Now().UTC()
		return nil
	})
}

func claimRunForDelete(runID, sessionID string) error {
	pid := os.Getpid()
	started, alive := processIdentity(pid)
	if !alive {
		return errors.New("cannot verify the deletion process state")
	}
	return mutateRun(runID, func(record *RunRecord) error {
		if record.SessionID != sessionID {
			return errDeleteSessionChanged
		}
		if recordIsAlive(*record) {
			return errDeleteSessionAlive
		}
		if recordHasActiveRecoveryClaim(*record) {
			return errRecoveryInProgress
		}
		record.RecoveryPID = pid
		record.RecoveryStartTime = started
		record.RecoveryClaimedAt = time.Now().UTC()
		return nil
	})
}

func releaseRunClaim(runID string) error {
	pid := os.Getpid()
	started, _ := processIdentity(pid)
	return mutateRun(runID, func(record *RunRecord) error {
		if record.RecoveryPID == pid && (started == 0 || record.RecoveryStartTime == started) {
			record.RecoveryPID = 0
			record.RecoveryStartTime = 0
			record.RecoveryClaimedAt = time.Time{}
		}
		return nil
	})
}

func recordHasActiveRecoveryClaim(record RunRecord) bool {
	return sameProcessAlive(record.RecoveryPID, record.RecoveryStartTime)
}

func removeRun(runID string) error {
	path, err := runFile(runID)
	if err != nil {
		return err
	}
	unlock, err := acquireFileLock(path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func removeClaimedRun(runID, sessionID string) error {
	path, err := runFile(runID)
	if err != nil {
		return err
	}
	unlock, err := acquireFileLock(path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()

	var record RunRecord
	if err := readJSON(path, &record); err != nil {
		return err
	}
	if record.SessionID != sessionID {
		return errDeleteSessionChanged
	}
	pid := os.Getpid()
	started, alive := processIdentity(pid)
	if !alive || record.RecoveryPID != pid || record.RecoveryStartTime != started {
		return errDeleteClaimLost
	}
	return os.Remove(path)
}

func acquireFileLock(path string) (func(), error) {
	for attempt := 0; attempt < 250; attempt++ {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_ = file.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > 2*time.Second {
			_ = os.Remove(path)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil, fmt.Errorf("timed out waiting for state-file lock: %s", path)
}

func readJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".guard-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
