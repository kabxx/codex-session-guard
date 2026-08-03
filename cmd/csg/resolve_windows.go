//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func resolveExplicitLaunchPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return findWindowsLaunchFile(abs)
}

func lookPathExcluding(name, excludedDir string) (string, error) {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if strings.TrimSpace(dir) == "" {
			dir = "."
		}
		for _, candidate := range windowsLaunchCandidates(filepath.Join(dir, name)) {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() && !isGuardInstallEntry(candidate, excludedDir) {
				return filepath.Abs(candidate)
			}
		}
	}
	return "", errors.New("no executable entry point found in PATH")
}

func findWindowsLaunchFile(path string) (string, error) {
	for _, candidate := range windowsLaunchCandidates(path) {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", os.ErrNotExist
}

func windowsLaunchCandidates(path string) []string {
	if filepath.Ext(path) != "" {
		return []string{path}
	}
	result := []string{path}
	for _, extension := range windowsPathExtensions() {
		result = append(result, path+extension)
	}
	return result
}

func windowsPathExtensions() []string {
	value := strings.TrimSpace(os.Getenv("PATHEXT"))
	if value == "" {
		value = ".COM;.EXE;.BAT;.CMD"
	}
	result := make([]string, 0, 4)
	for _, extension := range strings.Split(value, ";") {
		extension = strings.ToLower(strings.TrimSpace(extension))
		if extension == "" {
			continue
		}
		if !strings.HasPrefix(extension, ".") {
			extension = "." + extension
		}
		switch extension {
		case ".com", ".exe", ".bat", ".cmd":
			result = append(result, extension)
		}
	}
	return result
}
