//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func processIdentity(pid int) (uint64, bool) {
	started, state := queryLinuxProcess(pid)
	return started, state == processAlive
}

func processLiveness(pid int, expectedStart uint64) processState {
	started, state := queryLinuxProcess(pid)
	if state == processAlive && expectedStart != 0 && started != expectedStart {
		return processDead
	}
	return state
}

func queryLinuxProcess(pid int) (uint64, processState) {
	if pid <= 0 {
		return 0, processDead
	}
	probeErr := unix.Kill(pid, 0)
	if probeErr == unix.ESRCH {
		return 0, processDead
	}
	if probeErr != nil && probeErr != unix.EPERM {
		return 0, processUnknown
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		if unix.Kill(pid, 0) == unix.ESRCH {
			return 0, processDead
		}
		return 0, processUnknown
	}
	closing := strings.LastIndexByte(string(data), ')')
	if closing < 0 {
		return 0, processUnknown
	}
	fields := strings.Fields(string(data[closing+1:]))
	if len(fields) <= 19 {
		return 0, processUnknown
	}
	started, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, processUnknown
	}
	return started, processAlive
}

func nearestCodexAncestor() (int, uint64, bool) {
	return 0, 0, false
}
