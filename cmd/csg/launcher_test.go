package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveLaunchTargetAllowsUpstreamCodexInInstallDirectory(t *testing.T) {
	root := t.TempDir()
	installDir := filepath.Join(root, "guard-bin")
	upstreamDir := filepath.Join(root, "upstream-bin")
	if err := os.MkdirAll(installDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(upstreamDir, 0o700); err != nil {
		t.Fatal(err)
	}
	requested := "codex"
	guardName := requested
	upstreamName := requested
	if runtime.GOOS == "windows" {
		guardName += ".exe"
		upstreamName += ".cmd"
		t.Setenv("PATHEXT", ".EXE;.CMD")
	}
	writeExecutableFixture(t, filepath.Join(installDir, guardName))
	upstream := filepath.Join(upstreamDir, upstreamName)
	writeExecutableFixture(t, upstream)
	t.Setenv("PATH", installDir+string(os.PathListSeparator)+upstreamDir)

	target, err := resolveLaunchTarget(requested, root, installDir)
	if err != nil {
		t.Fatal(err)
	}
	if !sameResolvedPath(target.path, filepath.Join(installDir, guardName)) {
		t.Fatalf("resolved=%q want command in install directory", target.path)
	}
	if target.requested != requested || target.managed {
		t.Fatalf("unexpected target: %+v", target)
	}
}

func TestExplicitResumeSessionID(t *testing.T) {
	const sessionID = "019fc699-08ca-7f03-b620-98bd93511d80"
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "direct", args: []string{"resume", sessionID}, want: sessionID},
		{name: "global options", args: []string{"--profile", "work", "--strict-config", "--enable=hooks", "resume", sessionID}, want: sessionID},
		{name: "uppercase", args: []string{"resume", strings.ToUpper(sessionID)}, want: sessionID},
		{name: "session name", args: []string{"resume", "my-project"}},
		{name: "picker", args: []string{"resume"}},
		{name: "last picker", args: []string{"resume", "--last"}},
		{name: "prompt triggers hook", args: []string{"resume", sessionID, "continue"}},
		{name: "unknown global option", args: []string{"--future-option", "value", "resume", sessionID}},
		{name: "resume inside prompt", args: []string{"please", "resume", sessionID}},
		{name: "invalid uuid variant", args: []string{"resume", "019fc699-08ca-7f03-7620-98bd93511d80"}},
		{name: "nil uuid", args: []string{"resume", "00000000-0000-0000-0000-000000000000"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := explicitResumeSessionID(test.args); got != test.want {
				t.Fatalf("explicitResumeSessionID(%q) = %q, want %q", test.args, got, test.want)
			}
		})
	}
}

func TestResolveLaunchTargetAllowsOtherCommandInGuardInstallDirectory(t *testing.T) {
	installDir := t.TempDir()
	requested := "imba-codex"
	name := requested
	if runtime.GOOS == "windows" {
		name += ".cmd"
		t.Setenv("PATHEXT", ".EXE;.CMD")
	}
	targetPath := filepath.Join(installDir, name)
	writeExecutableFixture(t, targetPath)
	t.Setenv("PATH", installDir)

	target, err := resolveLaunchTarget(requested, installDir, installDir)
	if err != nil {
		t.Fatal(err)
	}
	if !sameResolvedPath(target.path, targetPath) {
		t.Fatalf("resolved=%q want=%q", target.path, targetPath)
	}
}

func TestResolveLaunchTargetRejectsExplicitGuardPath(t *testing.T) {
	installDir := t.TempDir()
	name := "csg"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(installDir, name)
	writeExecutableFixture(t, path)
	if _, err := resolveLaunchTarget(path, installDir, installDir); err == nil || !strings.Contains(err.Error(), "recursively") {
		t.Fatalf("expected recursion error, got %v", err)
	}
}

func TestTargetForRecordKeepsLegacyAndRejectsOtherPlatform(t *testing.T) {
	settings := Settings{RealCodexPath: filepath.Join(t.TempDir(), "codex"), InstallDir: filepath.Join(t.TempDir(), "bin")}
	legacy, err := targetForRecord(RunRecord{Version: 1}, settings)
	if err != nil || !legacy.managed || legacy.path != settings.RealCodexPath {
		t.Fatalf("legacy=%+v err=%v", legacy, err)
	}
	other := "linux"
	if runtime.GOOS == "linux" {
		other = "darwin"
	}
	record := RunRecord{Version: 2, Launcher: &LauncherRecord{Requested: "fork", ResolvedPath: "/tmp/fork", Platform: other}}
	if _, err := targetForRecord(record, settings); err == nil {
		t.Fatal("cross-platform record was accepted")
	}
}

func TestTargetForRecordRelocatesMissingBareCommand(t *testing.T) {
	root := t.TempDir()
	installDir := filepath.Join(root, "guard-bin")
	upstreamDir := filepath.Join(root, "new-upstream")
	if err := os.MkdirAll(installDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(upstreamDir, 0o700); err != nil {
		t.Fatal(err)
	}
	requested := "moved-codex"
	name := requested
	if runtime.GOOS == "windows" {
		name += ".cmd"
		t.Setenv("PATHEXT", ".CMD")
	}
	upstream := filepath.Join(upstreamDir, name)
	writeExecutableFixture(t, upstream)
	t.Setenv("PATH", upstreamDir)
	record := RunRecord{
		Version: 2,
		CWD:     root,
		Launcher: &LauncherRecord{
			Requested:    requested,
			ResolvedPath: filepath.Join(root, "old", name),
			Platform:     runtime.GOOS,
		},
	}
	target, err := targetForRecord(record, Settings{InstallDir: installDir})
	if err != nil {
		t.Fatal(err)
	}
	if !sameResolvedPath(target.path, upstream) {
		t.Fatalf("resolved=%q want=%q", target.path, upstream)
	}
}

func writeExecutableFixture(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func sameResolvedPath(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(leftAbs), filepath.Clean(rightAbs))
	}
	return filepath.Clean(leftAbs) == filepath.Clean(rightAbs)
}
