//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestGenericBatchLauncherLifecycle(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	built := filepath.Join(root, "codex-session-guard.exe")
	build := exec.Command("go", "build", "-o", built, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, output)
	}
	for _, name := range []string{"codex-session-guard.exe", "csg.exe", "fake-codex.exe"} {
		copyTestFile(t, built, filepath.Join(binDir, name))
	}
	writeWindowsCodexShim(t, binDir)

	fake := filepath.Join(binDir, "fake-codex.exe")
	foreground := testdataFile(t, "fake-launcher.cmd")
	background := testdataFile(t, "fake-background-launcher.cmd")

	tests := []struct {
		name       string
		launcher   string
		args       []string
		wantCode   int
		wantRuns   int
		wantOutput string
	}{
		{name: "foreground normal", launcher: foreground, args: []string{"--fake-session-end"}, wantCode: 0, wantRuns: 0},
		{name: "foreground crash", launcher: foreground, args: []string{"--fake-exit=7"}, wantCode: 7, wantRuns: 1, wantOutput: "session was preserved"},
		{name: "foreground crash before hook", launcher: foreground, args: []string{"--fake-no-hook", "--fake-exit=7"}, wantCode: 7, wantRuns: 0, wantOutput: "no recovery record was kept"},
		{name: "background normal", launcher: background, args: []string{"--fake-sleep-ms=200", "--fake-session-end"}, wantCode: 0, wantRuns: 0},
		{name: "background crash", launcher: background, args: []string{"--fake-sleep-ms=200", "--fake-exit=9"}, wantCode: 1, wantRuns: 1, wantOutput: "session was preserved"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			guardState := filepath.Join(root, strings.ReplaceAll(test.name, " ", "-"))
			t.Setenv("CODEX_SESSION_GUARD_HOME", guardState)
			t.Setenv("CSG_FAKE_CODEX", fake)
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			settings := Settings{Version: 1, InstallDir: binDir, RealCodexPath: fake}
			if err := writeJSONAtomic(mustSettingsPath(t), settings); err != nil {
				t.Fatal(err)
			}

			arguments := append([]string{"run", test.launcher}, test.args...)
			command := exec.Command(filepath.Join(binDir, "csg.exe"), arguments...)
			output, err := command.CombinedOutput()
			if got := exitCodeFromError(err); got != test.wantCode {
				t.Fatalf("exit=%d want=%d err=%v\n%s", got, test.wantCode, err, output)
			}
			if test.wantOutput != "" && !strings.Contains(string(output), test.wantOutput) {
				t.Fatalf("output missing %q:\n%s", test.wantOutput, output)
			}
			runs, readErr := readAllRuns()
			if readErr != nil || len(runs) != test.wantRuns {
				t.Fatalf("runs=%+v want=%d err=%v\n%s", runs, test.wantRuns, readErr, output)
			}
			if len(runs) == 1 && (runs[0].Launcher == nil || !strings.EqualFold(runs[0].Launcher.ResolvedPath, test.launcher)) {
				t.Fatalf("launcher was not persisted: %+v", runs[0].Launcher)
			}
		})
	}

	t.Run("direct codex is not tracked", func(t *testing.T) {
		guardState := filepath.Join(root, "direct-codex")
		argsPath := filepath.Join(root, "direct-codex-args.json")
		t.Setenv("CODEX_SESSION_GUARD_HOME", guardState)
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		settings := Settings{Version: 1, InstallDir: binDir, RealCodexPath: fake}
		if err := writeJSONAtomic(mustSettingsPath(t), settings); err != nil {
			t.Fatal(err)
		}
		writeFlag := "--fake-args-file=" + argsPath
		command := exec.Command("cmd.exe", "/d", "/c", filepath.Join(binDir, "codex.cmd"), writeFlag, "--fake-exit=7")
		output, err := command.CombinedOutput()
		if got := exitCodeFromError(err); got != 7 {
			t.Fatalf("exit=%d want=7 err=%v\n%s", got, err, output)
		}
		runs, readErr := readAllRuns()
		if readErr != nil || len(runs) != 0 {
			t.Fatalf("direct codex created monitoring records: %+v err=%v\n%s", runs, readErr, output)
		}
		data, err := os.ReadFile(argsPath)
		if err != nil {
			t.Fatal(err)
		}
		var received []string
		if err := json.Unmarshal(data, &received); err != nil {
			t.Fatal(err)
		}
		if expected := []string{writeFlag, "--fake-exit=7"}; !slices.Equal(received, expected) {
			t.Fatalf("direct codex arguments changed: received=%q expected=%q", received, expected)
		}
	})

	t.Run("launcher invokes guarded codex", func(t *testing.T) {
		guardState := filepath.Join(root, "nested-guard")
		t.Setenv("CODEX_SESSION_GUARD_HOME", guardState)
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		settings := Settings{Version: 1, InstallDir: binDir, RealCodexPath: fake}
		if err := writeJSONAtomic(mustSettingsPath(t), settings); err != nil {
			t.Fatal(err)
		}
		nested := testdataFile(t, "nested-codex-launcher.cmd")
		command := exec.Command(filepath.Join(binDir, "csg.exe"), "run", nested, "--fake-exit=7")
		output, err := command.CombinedOutput()
		if got := exitCodeFromError(err); got != 7 {
			t.Fatalf("exit=%d want=7 err=%v\n%s", got, err, output)
		}
		runs, readErr := readAllRuns()
		if readErr != nil || len(runs) != 1 {
			t.Fatalf("runs=%+v want one outer record err=%v\n%s", runs, readErr, output)
		}
		if runs[0].Launcher == nil || !strings.EqualFold(runs[0].Launcher.ResolvedPath, nested) {
			t.Fatalf("original launcher was not preserved: %+v", runs[0].Launcher)
		}
		if runs[0].SessionID == "" {
			t.Fatal("outer run did not receive the SessionStart binding")
		}
	})

	t.Run("argument fidelity", func(t *testing.T) {
		guardState := filepath.Join(root, "argument-fidelity")
		argsPath := filepath.Join(root, "received-args.json")
		t.Setenv("CODEX_SESSION_GUARD_HOME", guardState)
		t.Setenv("CSG_FAKE_CODEX", fake)
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		settings := Settings{Version: 1, InstallDir: binDir, RealCodexPath: fake}
		if err := writeJSONAtomic(mustSettingsPath(t), settings); err != nil {
			t.Fatal(err)
		}
		values := []string{"", "space value", `quote"inside`, `trail\`, `literal%PATH%`, `bang!`, `caret^`, `amp&ersand`, `pipe|`, `paren(x)`, `bracket[x]`, "semi;comma,star*question?"}
		writeFlag := "--fake-args-file=" + argsPath
		arguments := append([]string{"run", foreground, writeFlag}, values...)
		command := exec.Command(filepath.Join(binDir, "csg.exe"), arguments...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("launcher failed: %v\n%s", err, output)
		}
		data, err := os.ReadFile(argsPath)
		if err != nil {
			t.Fatal(err)
		}
		var received []string
		if err := json.Unmarshal(data, &received); err != nil {
			t.Fatal(err)
		}
		expected := append([]string{"--enable", "hooks", writeFlag}, values...)
		if !slices.Equal(received, expected) {
			t.Fatalf("arguments changed:\nreceived=%q\nexpected=%q", received, expected)
		}
	})
}

func TestWindowsBatchRejectsNewlineArgument(t *testing.T) {
	path := testdataFile(t, "fake-launcher.cmd")
	if _, err := windowsTargetCommand(path, []string{"line1\nline2"}); err == nil {
		t.Fatal("newline argument was accepted for a batch launcher")
	}
}

func TestWindowsJobKillsTreeWhenGuardIsKilled(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	built := filepath.Join(root, "codex-session-guard.exe")
	build := exec.Command("go", "build", "-o", built, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, output)
	}
	for _, name := range []string{"codex-session-guard.exe", "csg.exe", "fake-codex.exe"} {
		copyTestFile(t, built, filepath.Join(binDir, name))
	}

	guardState := filepath.Join(root, "state")
	t.Setenv("CODEX_SESSION_GUARD_HOME", guardState)
	t.Setenv("CSG_FAKE_CODEX", filepath.Join(binDir, "fake-codex.exe"))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	settings := Settings{Version: 1, InstallDir: binDir, RealCodexPath: filepath.Join(binDir, "fake-codex.exe")}
	if err := writeJSONAtomic(mustSettingsPath(t), settings); err != nil {
		t.Fatal(err)
	}

	launcher := testdataFile(t, "fake-launcher.cmd")
	command := exec.Command(filepath.Join(binDir, "csg.exe"), "run", launcher, "--fake-sleep-ms=30000")
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}

	var record RunRecord
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runs, _ := readAllRuns()
		if len(runs) == 1 && runs[0].SessionID != "" {
			record = runs[0]
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if record.RunID == "" {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("session did not bind:\n%s", output.String())
	}
	tracked := descendantIdentities(record.WrapperPID)
	for _, pid := range []int{record.WrapperPID, record.CodexPID} {
		if started, alive := processIdentity(pid); alive {
			tracked[pid] = started
		}
	}

	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		alive := false
		for pid, started := range tracked {
			if sameProcessAlive(pid, started) {
				alive = true
			}
		}
		if !alive {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	for pid, started := range tracked {
		if sameProcessAlive(pid, started) {
			t.Fatalf("process %d survived guard kill", pid)
		}
	}
	recoverable, err := recoveryCandidates()
	if err != nil || len(recoverable) != 1 {
		t.Fatalf("recoverable=%+v err=%v\n%s", recoverable, err, output.String())
	}
}

func TestGenericRecoveryUsesOriginalLauncher(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	built := filepath.Join(root, "codex-session-guard.exe")
	build := exec.Command("go", "build", "-o", built, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, output)
	}
	for _, name := range []string{"codex-session-guard.exe", "csg.exe", "fake-codex.exe"} {
		copyTestFile(t, built, filepath.Join(binDir, name))
	}

	guardState := filepath.Join(root, "state")
	fake := filepath.Join(binDir, "fake-codex.exe")
	t.Setenv("CODEX_SESSION_GUARD_HOME", guardState)
	t.Setenv("CSG_FAKE_CODEX", fake)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	settings := Settings{Version: 1, InstallDir: binDir, RealCodexPath: fake}
	if err := writeJSONAtomic(mustSettingsPath(t), settings); err != nil {
		t.Fatal(err)
	}

	launcher := testdataFile(t, "fake-launcher.cmd")
	sessionID := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	crash := exec.Command(filepath.Join(binDir, "csg.exe"), "run", launcher, "--fake-session="+sessionID, "--fake-exit=7")
	if output, err := crash.CombinedOutput(); exitCodeFromError(err) != 7 {
		t.Fatalf("crash exit=%d err=%v\n%s", exitCodeFromError(err), err, output)
	}
	recovery := exec.Command(filepath.Join(binDir, "csg.exe"), "resume", sessionID)
	output, err := recovery.CombinedOutput()
	if err != nil {
		t.Fatalf("recovery failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), launcher) || !strings.Contains(string(output), sessionID) {
		t.Fatalf("recovery did not identify original launcher/session:\n%s", output)
	}
	runs, readErr := readAllRuns()
	if readErr != nil || len(runs) != 0 {
		t.Fatalf("recovery left records: %+v err=%v\n%s", runs, readErr, output)
	}
}

func descendantIdentities(rootPID int) map[int]uint64 {
	entries := processSnapshot()
	selected := map[uint32]bool{uint32(rootPID): true}
	for changed := true; changed; {
		changed = false
		for pid, entry := range entries {
			if !selected[pid] && selected[entry.ParentProcessID] {
				selected[pid] = true
				changed = true
			}
		}
	}
	result := make(map[int]uint64, len(selected))
	for pid := range selected {
		if started, alive := processIdentity(int(pid)); alive {
			result[int(pid)] = started
		}
	}
	return result
}

func mustSettingsPath(t *testing.T) string {
	t.Helper()
	path, err := settingsPath()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func copyTestFile(t *testing.T, source, target string) {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o700); err != nil {
		t.Fatal(err)
	}
}

func writeWindowsCodexShim(t *testing.T, binDir string) {
	t.Helper()
	contents := "@echo off\r\n\"%~dp0fake-codex.exe\" %*\r\nexit /b %errorlevel%\r\n"
	if err := os.WriteFile(filepath.Join(binDir, "codex.cmd"), []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}
