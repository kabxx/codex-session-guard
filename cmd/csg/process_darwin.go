//go:build darwin

package main

import "golang.org/x/sys/unix"

func processIdentity(pid int) (uint64, bool) {
	started, state := queryDarwinProcess(pid)
	return started, state == processAlive
}

func processLiveness(pid int, expectedStart uint64) processState {
	started, state := queryDarwinProcess(pid)
	if state == processAlive && expectedStart != 0 && started != expectedStart {
		return processDead
	}
	return state
}

func queryDarwinProcess(pid int) (uint64, processState) {
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
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if unix.Kill(pid, 0) == unix.ESRCH {
			return 0, processDead
		}
		return 0, processUnknown
	}
	if int(info.Proc.P_pid) != pid {
		return 0, processDead
	}
	started := uint64(info.Proc.P_starttime.Sec)*1_000_000 + uint64(info.Proc.P_starttime.Usec)
	return started, processAlive
}

func nearestCodexAncestor() (int, uint64, bool) {
	return 0, 0, false
}
