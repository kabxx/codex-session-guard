package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func runFakeCodex(args []string) int {
	for _, arg := range args {
		if arg == "--version" || arg == "-V" {
			fmt.Println("codex-cli 0.144.5-fake")
			return 0
		}
	}
	sessionID := "11111111-2222-4333-8444-555555555555"
	secondSessionID := ""
	exitCode := 0
	sleepMilliseconds := 0
	skipHook := false
	emitSessionEnd := false
	argsFile := ""
	for _, arg := range args {
		if strings.HasPrefix(arg, "--fake-session=") {
			sessionID = strings.TrimPrefix(arg, "--fake-session=")
		}
		if strings.HasPrefix(arg, "--fake-exit=") {
			exitCode, _ = strconv.Atoi(strings.TrimPrefix(arg, "--fake-exit="))
		}
		if strings.HasPrefix(arg, "--fake-sleep-ms=") {
			sleepMilliseconds, _ = strconv.Atoi(strings.TrimPrefix(arg, "--fake-sleep-ms="))
		}
		if strings.HasPrefix(arg, "--fake-second-session=") {
			secondSessionID = strings.TrimPrefix(arg, "--fake-second-session=")
		}
		if arg == "--fake-no-hook" {
			skipHook = true
		}
		if arg == "--fake-session-end" {
			emitSessionEnd = true
		}
		if strings.HasPrefix(arg, "--fake-args-file=") {
			argsFile = strings.TrimPrefix(arg, "--fake-args-file=")
		}
	}
	if os.Getenv("CSG_FAKE_NO_HOOK") == "1" {
		skipHook = true
	}
	if value := os.Getenv("CSG_FAKE_SLEEP_MS"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			sleepMilliseconds = parsed
		}
	}
	if argsFile != "" {
		data, err := json.Marshal(args)
		if err != nil || os.WriteFile(argsFile, data, 0o600) != nil {
			return 93
		}
	}
	for index, arg := range args {
		if arg == "resume" && index+1 < len(args) {
			sessionID = args[index+1]
			break
		}
	}
	runID := os.Getenv(runIDEnv)
	if validRunID(runID) && !skipHook {
		if err := emitFakeHook("SessionStart", sessionID, "startup"); err != nil {
			fmt.Fprintln(os.Stderr, "fake hook failed:", err)
			return 90
		}
		if secondSessionID != "" {
			if err := emitFakeHook("SessionStart", secondSessionID, "clear"); err != nil {
				fmt.Fprintln(os.Stderr, "fake clear hook failed:", err)
				return 91
			}
		}
	}
	if sleepMilliseconds > 0 {
		time.Sleep(time.Duration(sleepMilliseconds) * time.Millisecond)
	}
	if validRunID(runID) && !skipHook && emitSessionEnd {
		if err := emitFakeHook("SessionEnd", sessionID, ""); err != nil {
			fmt.Fprintln(os.Stderr, "fake session end hook failed:", err)
			return 92
		}
	}
	return exitCode
}

func emitFakeHook(eventName, sessionID, source string) error {
	payload, err := json.Marshal(HookInput{
		SessionID:      sessionID,
		TranscriptPath: `C:\fake\rollout.jsonl`,
		CWD:            mustGetwd(),
		HookEventName:  eventName,
		Source:         source,
	})
	if err != nil {
		return err
	}
	command := exec.Command("codex-session-guard", "hook")
	command.Stdin = strings.NewReader(string(payload))
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func mustGetwd() string {
	cwd, _ := os.Getwd()
	return cwd
}
