//go:build !windows

package main

import (
	"os"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

func internalMain(mode string) int {
	if mode != unixKeeperMode {
		return 2
	}
	ownerPID, ownerErr := strconv.Atoi(os.Getenv(unixOwnerPIDEnv))
	ownerStart, startErr := strconv.ParseUint(os.Getenv(unixOwnerStartEnv), 10, 64)
	pgid, groupErr := strconv.Atoi(os.Getenv(unixProcessGroupEnv))
	if ownerErr != nil || startErr != nil || groupErr != nil || ownerPID <= 0 || pgid <= 0 {
		return 2
	}
	for sameProcessAlive(ownerPID, ownerStart) {
		// Exit as soon as the original group is gone. This avoids retaining a
		// stale PGID that could later be reused by an unrelated process group.
		if !processGroupAlive(pgid) {
			return 0
		}
		time.Sleep(50 * time.Millisecond)
	}
	if processGroupAlive(pgid) {
		_ = unix.Kill(-pgid, unix.SIGKILL)
	}
	return 0
}
