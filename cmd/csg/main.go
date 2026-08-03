package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	programVersion    = "0.7.0"
	hookCommand       = "codex-session-guard hook"
	legacyHookCommand = "codex-session-guard.exe hook"
	hookMatcher       = "startup|resume|clear|compact"
	runIDEnv          = "CODEX_SESSION_GUARD_RUN_ID"
	internalModeEnv   = "CODEX_SESSION_GUARD_INTERNAL_MODE"
)

func main() {
	if mode := os.Getenv(internalModeEnv); mode != "" {
		os.Exit(internalMain(mode))
	}
	base := executableName(os.Args[0])
	args := os.Args[1:]

	// Used only by the isolated integration test.
	if base == "fake-codex" {
		os.Exit(runFakeCodex(args))
	}

	var code int
	switch base {
	case "csg":
		code = csgMain(args)
	case "codex-session-guard":
		code = managementMain(args)
	default:
		fmt.Fprintln(os.Stderr, "Codex Session Guard: unknown executable entry point:", filepath.Base(os.Args[0]))
		code = 2
	}
	os.Exit(code)
}

func csgMain(args []string) int {
	if len(args) == 0 {
		printCSGUsage()
		return 0
	}

	switch args[0] {
	case "run":
		return runMain(args[1:])
	case "list":
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "csg list does not accept arguments")
			return 2
		}
		return listSessionsMain()
	case "resume":
		return resumeMain(args[1:])
	case "delete":
		return deleteMain(args[1:])
	case "doctor":
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "csg doctor does not accept arguments")
			return 2
		}
		return doctorMain()
	case "version", "--version", "-V":
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "csg version does not accept arguments")
			return 2
		}
		fmt.Println("csg " + programVersion)
		return 0
	case "help", "--help", "-h":
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "csg help does not accept arguments")
			return 2
		}
		printCSGUsage()
		return 0
	default:
		fmt.Fprintln(os.Stderr, "Unknown csg command:", args[0])
		fmt.Fprintln(os.Stderr, "To launch a Codex-compatible command, use: csg run <command-or-path> [args...]")
		return 2
	}
}

func printCSGUsage() {
	fmt.Println(`Codex Session Guard

Usage:
  csg run <command-or-path> [args...]
  csg list
  csg resume <session-id>
  csg delete <session-id>
  csg doctor
  csg version
  csg help`)
}

func executableName(path string) string {
	base := strings.ToLower(filepath.Base(path))
	return strings.TrimSuffix(base, ".exe")
}

func managementMain(args []string) int {
	if len(args) == 0 {
		printManagementUsage()
		return 0
	}

	switch args[0] {
	case "install":
		return installMain(args[1:])
	case "uninstall":
		return uninstallMain(args[1:])
	case "doctor":
		return doctorMain()
	case "hook":
		return hookMain(os.Stdin)
	case "version", "--version", "-V":
		fmt.Println("codex-session-guard " + programVersion)
		return 0
	case "help", "--help", "-h":
		printManagementUsage()
		return 0
	default:
		fmt.Fprintln(os.Stderr, "Unknown command:", args[0])
		printManagementUsage()
		return 2
	}
}

func printManagementUsage() {
	fmt.Println(`Codex Session Guard management interface

Usage:
  codex-session-guard doctor
  codex-session-guard install --bin-dir <directory>
  codex-session-guard uninstall
  codex-session-guard version

For regular use, run: csg help`)
}
