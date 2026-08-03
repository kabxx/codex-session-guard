package main

import "errors"

type processSpec struct {
	target launchTarget
	args   []string
	cwd    string
	env    []string
	runID  string
}

type processResult struct {
	Started      bool
	PID          int
	ExitCode     int
	Err          error
	Interrupted  bool
	Detached     bool
	SessionEnded bool
}

func exitCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	type exitCoder interface {
		ExitCode() int
	}
	var coder exitCoder
	if errors.As(err, &coder) {
		return coder.ExitCode()
	}
	return 1
}

func sessionEndWasSeen(runID string) bool {
	record, err := loadRun(runID)
	return err == nil && !record.SessionEndedAt.IsZero()
}
