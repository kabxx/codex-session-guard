package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

func runGuarded(target launchTarget, args []string, workingDir string, settings Settings) int {
	if info, err := os.Stat(target.path); err != nil || info.IsDir() {
		fmt.Fprintln(os.Stderr, "Codex Session Guard: launcher not found:", target.path)
		if target.managed {
			fmt.Fprintln(os.Stderr, "Run the installer again.")
		}
		return 1
	}

	runID, err := newRunID()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Codex Session Guard:", err)
		return 1
	}
	cwd, _ := os.Getwd()
	if workingDir != "" {
		cwd = workingDir
	}
	wrapperStart, _ := processIdentity(os.Getpid())
	record := RunRecord{
		Version:          2,
		RunID:            runID,
		StartedAt:        time.Now().UTC(),
		CWD:              cwd,
		WrapperPID:       os.Getpid(),
		WrapperStartTime: wrapperStart,
		Launcher:         target.persistedLauncher(),
	}
	if err := saveNewRun(record); err != nil {
		fmt.Fprintln(os.Stderr, "Codex Session Guard: failed to create a session record:", err)
		return 1
	}

	realArgs := append([]string{"--enable", "hooks"}, args...)
	result := superviseProcess(processSpec{
		target: target,
		args:   realArgs,
		cwd:    cwd,
		env:    childEnvironment(settings, runID, target.managed),
		runID:  runID,
	}, func(pid int, started uint64) {
		_ = updateRun(runID, func(current *RunRecord) {
			current.CodexPID = pid
			current.CodexStartTime = started
		})
	})
	if !result.Started {
		_ = removeRun(runID)
		fmt.Fprintln(os.Stderr, "Codex Session Guard: launch failed:", result.Err)
		return 1
	}
	cleanExit := result.Err == nil || result.Interrupted
	if result.Detached && !result.SessionEnded && !result.Interrupted {
		cleanExit = false
		if result.Err == nil {
			result.Err = errors.New("launcher exited before the supervised Codex process and no SessionEnd hook was received")
			result.ExitCode = 1
		}
	}
	if cleanExit {
		if err := removeRun(runID); err != nil {
			fmt.Fprintln(os.Stderr, "Codex Session Guard: failed to remove the clean-exit record:", err)
		}
		return result.ExitCode
	}

	if err := updateRun(runID, func(current *RunRecord) {
		current.EndedAt = time.Now().UTC()
		current.ExitCode = result.ExitCode
		current.ExitError = result.Err.Error()
	}); err != nil {
		fmt.Fprintln(os.Stderr, "Codex Session Guard: failed to save crash information:", err)
	}
	latest, loadErr := waitForSessionBinding(runID, 2*time.Second)
	if loadErr != nil || latest.SessionID == "" {
		if err := removeRun(runID); err != nil {
			fmt.Fprintln(os.Stderr, "Codex Session Guard: no session ID was received and the temporary run record could not be removed:", err)
		} else {
			fmt.Fprintln(os.Stderr, "Codex Session Guard: the target crashed before a session ID was received; no recovery record was kept.")
		}
	} else {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Codex crashed and the session was preserved. Run csg list, then csg resume <session-id> to recover it.")
	}
	return result.ExitCode
}

func waitForSessionBinding(runID string, timeout time.Duration) (RunRecord, error) {
	deadline := time.Now().Add(timeout)
	for {
		record, err := loadRun(runID)
		if err != nil || record.SessionID != "" || time.Now().After(deadline) {
			return record, err
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func childEnvironment(settings Settings, runID string, managed bool) []string {
	env := os.Environ()
	env = setEnv(env, runIDEnv, runID)
	env = setEnv(env, "PATH", settings.InstallDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if !managed {
		return env
	}
	if settings.ManagedPackageRoot != "" {
		env = setEnv(env, "CODEX_MANAGED_PACKAGE_ROOT", settings.ManagedPackageRoot)
	}
	for _, key := range []string{"CODEX_MANAGED_BY_NPM", "CODEX_MANAGED_BY_PNPM", "CODEX_MANAGED_BY_BUN"} {
		env = removeEnv(env, key)
	}
	if settings.ManagedBy != "" {
		env = setEnv(env, "CODEX_MANAGED_BY_"+strings.ToUpper(settings.ManagedBy), "1")
	}
	return env
}

func setEnv(env []string, key, value string) []string {
	env = removeEnv(env, key)
	return append(env, key+"="+value)
}

func removeEnv(env []string, key string) []string {
	prefix := strings.ToUpper(key) + "="
	result := env[:0]
	for _, entry := range env {
		if !strings.HasPrefix(strings.ToUpper(entry), prefix) {
			result = append(result, entry)
		}
	}
	return result
}
