package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type launchTarget struct {
	requested string
	path      string
	managed   bool
}

func runMain(args []string) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Println(`Usage:
  csg run <command-or-path> [args...]
  csg run -- <command-or-path> [args...]

Arguments after the target are used only for this launch. Resume always invokes the same launcher with resume <session-id>.`)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	if args[0] == "--" {
		args = args[1:]
		if len(args) == 0 {
			fmt.Fprintln(os.Stderr, "csg run: expected a command or path after --")
			return 2
		}
	}

	settings, err := loadSettings()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Codex Session Guard:", err)
		return 1
	}
	cwd, _ := os.Getwd()
	target, err := resolveLaunchTarget(args[0], cwd, settings.InstallDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "csg run:", err)
		return 1
	}
	return runGuarded(target, args[1:], "", settings)
}

func resolveLaunchTarget(requested, cwd, installDir string) (launchTarget, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" || strings.IndexByte(requested, 0) >= 0 {
		return launchTarget{}, errors.New("launch command is empty or invalid")
	}

	var resolved string
	var err error
	if hasPathSyntax(requested) {
		resolved = requested
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(cwd, resolved)
		}
		resolved, err = resolveExplicitLaunchPath(resolved)
	} else {
		resolved, err = lookPathExcluding(requested, installDir)
	}
	if err != nil {
		return launchTarget{}, fmt.Errorf("launcher %q not found: %w", requested, err)
	}
	resolved = filepath.Clean(resolved)
	info, err := os.Stat(resolved)
	if err != nil {
		return launchTarget{}, fmt.Errorf("cannot access launcher %q: %w", resolved, err)
	}
	if info.IsDir() {
		return launchTarget{}, fmt.Errorf("launcher is a directory, not a file: %s", resolved)
	}
	if isGuardInstallEntry(resolved, installDir) {
		return launchTarget{}, fmt.Errorf("refusing to recursively invoke a Codex Session Guard entry point: %s", resolved)
	}
	if self, selfErr := os.Executable(); selfErr == nil {
		if selfInfo, statErr := os.Stat(self); statErr == nil && os.SameFile(info, selfInfo) {
			return launchTarget{}, errors.New("refusing to use Codex Session Guard itself as the launch target")
		}
	}

	return launchTarget{requested: requested, path: resolved}, nil
}

func (target launchTarget) persistedLauncher() *LauncherRecord {
	if target.managed {
		return nil
	}
	return &LauncherRecord{
		Requested:    target.requested,
		ResolvedPath: target.path,
		Platform:     runtime.GOOS,
	}
}

func targetForRecord(record RunRecord, settings Settings) (launchTarget, error) {
	if record.Launcher == nil {
		if strings.TrimSpace(settings.RealCodexPath) == "" {
			return launchTarget{}, errors.New("legacy run record has no original Codex path and cannot be resumed automatically")
		}
		return launchTarget{requested: "codex", path: settings.RealCodexPath, managed: true}, nil
	}
	if record.Launcher.Platform != "" && record.Launcher.Platform != runtime.GOOS {
		return launchTarget{}, fmt.Errorf("session was launched on %s and cannot be resumed automatically on %s", record.Launcher.Platform, runtime.GOOS)
	}
	target := launchTarget{requested: record.Launcher.Requested, path: filepath.Clean(record.Launcher.ResolvedPath)}
	if target.path == "" || !filepath.IsAbs(target.path) {
		return launchTarget{}, errors.New("run record has no launcher path")
	}
	info, err := os.Stat(target.path)
	if err != nil || info.IsDir() || isGuardInstallEntry(target.path, settings.InstallDir) {
		cwd := record.CWD
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		refreshed, refreshErr := resolveLaunchTarget(record.Launcher.Requested, cwd, settings.InstallDir)
		if refreshErr == nil {
			return refreshed, nil
		}
		if err != nil {
			return launchTarget{}, fmt.Errorf("original launcher no longer exists and cannot be resolved by its original command: %s", target.path)
		}
		return launchTarget{}, fmt.Errorf("run record contains an invalid launcher: %s", target.path)
	}
	if self, selfErr := os.Executable(); selfErr == nil {
		if selfInfo, statErr := os.Stat(self); statErr == nil && os.SameFile(info, selfInfo) {
			return launchTarget{}, errors.New("run record points to Codex Session Guard itself")
		}
	}
	return target, nil
}

func launcherLabel(record RunRecord) string {
	if record.Launcher == nil {
		return "codex"
	}
	if requested := strings.TrimSpace(record.Launcher.Requested); requested != "" {
		return requested
	}
	return filepath.Base(record.Launcher.ResolvedPath)
}

func hasPathSyntax(value string) bool {
	return filepath.IsAbs(value) || filepath.VolumeName(value) != "" || strings.ContainsAny(value, `/\\`)
}

func isGuardInstallEntry(path, installDir string) bool {
	if strings.TrimSpace(installDir) == "" {
		return false
	}
	pathAbs, pathErr := filepath.Abs(path)
	dirAbs, dirErr := filepath.Abs(installDir)
	if pathErr != nil || dirErr != nil {
		return false
	}
	for _, name := range installedCommandNames() {
		if pathsEqual(pathAbs, filepath.Join(dirAbs, name)) {
			return true
		}
	}
	if pathsEqual(pathAbs, filepath.Join(dirAbs, commandName("codex-recover"))) {
		return true
	}
	return false
}
