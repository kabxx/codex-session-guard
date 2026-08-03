//go:build !windows

package main

import (
	"os"
	"os/exec"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func superviseProcess(spec processSpec, onStart func(int, uint64)) processResult {
	cmd := exec.Command(spec.target.path, spec.args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = spec.cwd
	cmd.Env = spec.env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return processResult{Err: err, ExitCode: 1}
	}
	pid := cmd.Process.Pid
	restoreTerminal := giveTerminalTo(pid)
	defer restoreTerminal()
	started, _ := processIdentity(pid)
	onStart(pid, started)

	var interrupted atomic.Bool
	signals := make(chan os.Signal, 8)
	done := make(chan struct{})
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for {
			select {
			case received := <-signals:
				if received == os.Interrupt {
					interrupted.Store(true)
				}
				if unixSignal, ok := received.(syscall.Signal); ok {
					_ = unix.Kill(-pid, unixSignal)
				}
			case <-done:
				return
			}
		}
	}()

	waitErr := cmd.Wait()
	rootInterrupted := interrupted.Load() || exitedBySignal(waitErr, syscall.SIGINT)
	detached, sessionEnded := waitForUnixProcessGroup(pid, spec.runID, waitErr != nil || rootInterrupted)
	close(done)
	signal.Stop(signals)
	return processResult{
		Started:      true,
		PID:          pid,
		ExitCode:     exitCodeFromError(waitErr),
		Err:          waitErr,
		Interrupted:  rootInterrupted,
		Detached:     detached,
		SessionEnded: sessionEnded,
	}
}

func waitForUnixProcessGroup(pgid int, runID string, forceStop bool) (bool, bool) {
	if !processGroupAlive(pgid) {
		return false, sessionEndWasSeen(runID)
	}
	detached := true
	deadline := time.Time{}
	if forceStop {
		deadline = time.Now().Add(3 * time.Second)
	}
	for {
		sessionEnded := sessionEndWasSeen(runID)
		if sessionEnded && deadline.IsZero() {
			deadline = time.Now().Add(2 * time.Second)
		}
		if !processGroupAlive(pgid) {
			return detached, sessionEnded
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			_ = unix.Kill(-pgid, unix.SIGKILL)
			return detached, sessionEnded
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func processGroupAlive(pgid int) bool {
	err := unix.Kill(-pgid, 0)
	return err == nil || err == unix.EPERM
}

func giveTerminalTo(pgid int) func() {
	fd := int(os.Stdin.Fd())
	original, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	if err != nil {
		return func() {}
	}
	signal.Ignore(syscall.SIGTTOU)
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, pgid); err != nil {
		signal.Reset(syscall.SIGTTOU)
		return func() {}
	}
	return func() {
		_ = unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, original)
		signal.Reset(syscall.SIGTTOU)
	}
}

func exitedBySignal(err error, expected syscall.Signal) bool {
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		return false
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	return ok && status.Signaled() && status.Signal() == expected
}
