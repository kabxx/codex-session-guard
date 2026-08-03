package main

type processState uint8

const (
	processDead processState = iota
	processAlive
	processUnknown
)

// Unknown is treated as alive. A permissions or transient inspection failure
// must never make an active session appear recoverable in a second terminal.
func sameProcessAlive(pid int, expectedStart uint64) bool {
	return processLiveness(pid, expectedStart) != processDead
}
