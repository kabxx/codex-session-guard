//go:build linux || darwin

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

func TestGenericUnixLauncherLifecycle(t *testing.T) {
	root, binDir, fake, csg := buildUnixTestCommands(t)
	foreground := writeUnixLauncher(t, root, "foreground.sh", `#!/bin/sh
exec "$CSG_FAKE_CODEX" "$@"
`)
	background := writeUnixLauncher(t, root, "background.sh", `#!/bin/sh
"$CSG_FAKE_CODEX" "$@" &
exit 0
`)
	nested := filepath.Join(root, "nested.sh")
	copyUnixTestFile(t, testdataFile(t, "nested-codex-launcher.sh"), nested)

	tests := []struct {
		name     string
		launcher string
		args     []string
		wantCode int
		wantRuns int
	}{
		{name: "foreground normal", launcher: foreground, args: []string{"--fake-session-end"}, wantCode: 0, wantRuns: 0},
		{name: "foreground crash", launcher: foreground, args: []string{"--fake-exit=7"}, wantCode: 7, wantRuns: 1},
		{name: "foreground crash before hook", launcher: foreground, args: []string{"--fake-no-hook", "--fake-exit=7"}, wantCode: 7, wantRuns: 0},
		{name: "background normal", launcher: background, args: []string{"--fake-sleep-ms=200", "--fake-session-end"}, wantCode: 0, wantRuns: 0},
		{name: "background crash", launcher: background, args: []string{"--fake-sleep-ms=200", "--fake-exit=9"}, wantCode: 1, wantRuns: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			guardState := filepath.Join(root, strings.ReplaceAll(test.name, " ", "-"))
			configureUnixTest(t, guardState, binDir, fake)
			command := exec.Command(csg, append([]string{"run", test.launcher}, test.args...)...)
			output, err := command.CombinedOutput()
			if got := exitCodeFromError(err); got != test.wantCode {
				t.Fatalf("exit=%d want=%d err=%v\n%s", got, test.wantCode, err, output)
			}
			runs, readErr := readAllRuns()
			if readErr != nil || len(runs) != test.wantRuns {
				t.Fatalf("runs=%+v want=%d err=%v\n%s", runs, test.wantRuns, readErr, output)
			}
		})
	}

	t.Run("direct codex is not tracked", func(t *testing.T) {
		guardState := filepath.Join(root, "direct-codex")
		argsPath := filepath.Join(root, "direct-codex-args.json")
		configureUnixTest(t, guardState, binDir, fake)
		writeFlag := "--fake-args-file=" + argsPath
		command := exec.Command(filepath.Join(binDir, "codex"), writeFlag, "--fake-exit=7")
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
		configureUnixTest(t, guardState, binDir, fake)
		command := exec.Command(csg, "run", nested, "--fake-exit=7")
		output, err := command.CombinedOutput()
		if got := exitCodeFromError(err); got != 7 {
			t.Fatalf("exit=%d want=7 err=%v\n%s", got, err, output)
		}
		runs, readErr := readAllRuns()
		if readErr != nil || len(runs) != 1 {
			t.Fatalf("runs=%+v want one outer record err=%v\n%s", runs, readErr, output)
		}
		if runs[0].Launcher == nil || runs[0].Launcher.ResolvedPath != nested {
			t.Fatalf("original launcher was not preserved: %+v", runs[0].Launcher)
		}
		if runs[0].SessionID == "" {
			t.Fatal("outer run did not receive the SessionStart binding")
		}
	})

	t.Run("recovery uses original launcher", func(t *testing.T) {
		guardState := filepath.Join(root, "recovery")
		configureUnixTest(t, guardState, binDir, fake)
		sessionID := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
		crash := exec.Command(csg, "run", foreground, "--fake-session="+sessionID, "--fake-exit=7")
		if output, err := crash.CombinedOutput(); exitCodeFromError(err) != 7 {
			t.Fatalf("crash failed: %v\n%s", err, output)
		}
		output, err := exec.Command(csg, "resume", sessionID).CombinedOutput()
		if err != nil || !strings.Contains(string(output), foreground) {
			t.Fatalf("recovery failed: %v\n%s", err, output)
		}
		runs, readErr := readAllRuns()
		if readErr != nil || len(runs) != 0 {
			t.Fatalf("recovery left records: %+v err=%v", runs, readErr)
		}
	})
}

func TestUnixKeeperKillsGroupWhenGuardIsKilled(t *testing.T) {
	root, binDir, fake, csg := buildUnixTestCommands(t)
	launcher := writeUnixLauncher(t, root, "keeper.sh", `#!/bin/sh
exec "$CSG_FAKE_CODEX" "$@"
`)
	guardState := filepath.Join(root, "keeper-state")
	configureUnixTest(t, guardState, binDir, fake)

	command := exec.Command(csg, "run", launcher, "--fake-sleep-ms=30000")
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
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	deadline = time.Now().Add(5 * time.Second)
	for sameProcessAlive(record.CodexPID, record.CodexStartTime) && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if sameProcessAlive(record.CodexPID, record.CodexStartTime) {
		t.Fatalf("child survived guard kill: pid=%d", record.CodexPID)
	}
	recoverable, err := recoveryCandidates()
	if err != nil || len(recoverable) != 1 {
		t.Fatalf("recoverable=%+v err=%v\n%s", recoverable, err, output.String())
	}
}

func buildUnixTestCommands(t *testing.T) (root, binDir, fake, csg string) {
	t.Helper()
	root = t.TempDir()
	binDir = filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	built := filepath.Join(root, "codex-session-guard")
	if output, err := exec.Command("go", "build", "-o", built, ".").CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, output)
	}
	for _, name := range []string{"codex-session-guard", "csg", "fake-codex"} {
		copyUnixTestFile(t, built, filepath.Join(binDir, name))
	}
	writeUnixLauncher(t, binDir, "codex", `#!/bin/sh
exec "$(dirname "$0")/fake-codex" "$@"
`)
	return root, binDir, filepath.Join(binDir, "fake-codex"), filepath.Join(binDir, "csg")
}

func configureUnixTest(t *testing.T, guardState, binDir, fake string) {
	t.Helper()
	t.Setenv("CODEX_SESSION_GUARD_HOME", guardState)
	t.Setenv("CSG_FAKE_CODEX", fake)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	settings := Settings{Version: 1, InstallDir: binDir, RealCodexPath: fake}
	if err := writeJSONAtomic(mustUnixSettingsPath(t), settings); err != nil {
		t.Fatal(err)
	}
}

func writeUnixLauncher(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func copyUnixTestFile(t *testing.T, source, target string) {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o700); err != nil {
		t.Fatal(err)
	}
}

func mustUnixSettingsPath(t *testing.T) string {
	t.Helper()
	path, err := settingsPath()
	if err != nil {
		t.Fatal(err)
	}
	return path
}
