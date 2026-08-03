//go:build !windows

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
	if !isUnixExecutable(abs) {
		return "", os.ErrPermission
	}
	return abs, nil
}

func lookPathExcluding(name, excludedDir string) (string, error) {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if strings.TrimSpace(dir) == "" {
			dir = "."
		}
		candidate := filepath.Join(dir, name)
		if isUnixExecutable(candidate) && !isGuardInstallEntry(candidate, excludedDir) {
			return filepath.Abs(candidate)
		}
	}
	return "", errors.New("no executable entry point found in PATH")
}

func isUnixExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0
}
