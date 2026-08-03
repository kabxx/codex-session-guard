//go:build windows

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	jobKeeperMode  = "windows-job-keeper"
	jobHandleEnv   = "CODEX_SESSION_GUARD_JOB_HANDLE"
	jobOwnerPIDEnv = "CODEX_SESSION_GUARD_JOB_OWNER_PID"
)

type jobBasicAccountingInformation struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

func superviseProcess(spec processSpec, onStart func(int, uint64)) processResult {
	job, err := startWindowsJobKeeper()
	if err != nil {
		return processResult{Err: fmt.Errorf("failed to establish Windows process-tree supervision: %w", err), ExitCode: 1}
	}
	defer windows.CloseHandle(job)

	cmd, err := windowsTargetCommand(spec.target.path, spec.args)
	if err != nil {
		return processResult{Err: err, ExitCode: 1}
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = spec.cwd
	cmd.Env = spec.env
	if err := cmd.Start(); err != nil {
		return processResult{Err: err, ExitCode: 1}
	}
	started, _ := processIdentity(cmd.Process.Pid)
	onStart(cmd.Process.Pid, started)

	var interrupted atomic.Bool
	interrupts := make(chan os.Signal, 4)
	done := make(chan struct{})
	signal.Notify(interrupts, os.Interrupt)
	go func() {
		for {
			select {
			case <-interrupts:
				interrupted.Store(true)
			case <-done:
				return
			}
		}
	}()

	waitErr := cmd.Wait()
	close(done)
	signal.Stop(interrupts)

	detached, sessionEnded := waitForWindowsJob(job, spec.runID, waitErr != nil || interrupted.Load())
	return processResult{
		Started:      true,
		PID:          cmd.Process.Pid,
		ExitCode:     exitCodeFromError(waitErr),
		Err:          waitErr,
		Interrupted:  interrupted.Load(),
		Detached:     detached,
		SessionEnded: sessionEnded,
	}
}

func startWindowsJobKeeper() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	cleanup := func() {
		_ = windows.CloseHandle(job)
	}

	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
		cleanup()
		return 0, err
	}
	if err := windows.SetHandleInformation(job, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
		cleanup()
		return 0, err
	}

	self, err := os.Executable()
	if err != nil {
		cleanup()
		return 0, err
	}
	keeper := exec.Command(self)
	keeper.Env = os.Environ()
	keeper.Env = setEnv(keeper.Env, internalModeEnv, jobKeeperMode)
	keeper.Env = setEnv(keeper.Env, jobHandleEnv, strconv.FormatUint(uint64(job), 10))
	keeper.Env = setEnv(keeper.Env, jobOwnerPIDEnv, strconv.Itoa(os.Getpid()))
	keeper.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags:              windows.CREATE_NO_WINDOW,
		AdditionalInheritedHandles: []syscall.Handle{syscall.Handle(job)},
	}
	if err := keeper.Start(); err != nil {
		cleanup()
		return 0, err
	}
	_ = windows.SetHandleInformation(job, windows.HANDLE_FLAG_INHERIT, 0)
	go func() { _ = keeper.Wait() }()

	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		_ = keeper.Process.Kill()
		cleanup()
		return 0, err
	}
	return job, nil
}

func internalMain(mode string) int {
	if mode != jobKeeperMode {
		return 2
	}
	handleValue, err := strconv.ParseUint(os.Getenv(jobHandleEnv), 10, 64)
	if err != nil || handleValue == 0 {
		return 2
	}
	job := windows.Handle(handleValue)
	defer windows.CloseHandle(job)
	ownerPID, err := strconv.Atoi(os.Getenv(jobOwnerPIDEnv))
	if err != nil || ownerPID <= 0 {
		return 2
	}
	owner, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(ownerPID))
	if err == nil {
		_, _ = windows.WaitForSingleObject(owner, windows.INFINITE)
		_ = windows.CloseHandle(owner)
	}
	return 0
}

func waitForWindowsJob(job windows.Handle, runID string, forceStop bool) (bool, bool) {
	active, err := activeJobProcesses(job)
	if err != nil || active <= 1 {
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
		active, err = activeJobProcesses(job)
		if err != nil || active <= 1 {
			return detached, sessionEnded
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			_ = terminateJobChildren(job)
			return detached, sessionEnded
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func activeJobProcesses(job windows.Handle) (uint32, error) {
	var info jobBasicAccountingInformation
	err := windows.QueryInformationJobObject(
		job,
		windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
		nil,
	)
	return info.ActiveProcesses, err
}

func terminateJobChildren(job windows.Handle) error {
	pids, err := jobProcessIDs(job)
	if err != nil {
		return err
	}
	var joined error
	current := uint32(os.Getpid())
	for _, pid := range pids {
		if pid == 0 || pid == current {
			continue
		}
		process, openErr := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
		if openErr != nil {
			joined = errors.Join(joined, openErr)
			continue
		}
		terminateErr := windows.TerminateProcess(process, 1)
		_ = windows.CloseHandle(process)
		joined = errors.Join(joined, terminateErr)
	}
	return joined
}

func jobProcessIDs(job windows.Handle) ([]uint32, error) {
	for size := 4096; size <= 1<<20; size *= 2 {
		buffer := make([]byte, size)
		err := windows.QueryInformationJobObject(
			job,
			windows.JobObjectBasicProcessIdList,
			uintptr(unsafe.Pointer(&buffer[0])),
			uint32(len(buffer)),
			nil,
		)
		if err != nil {
			if errors.Is(err, windows.ERROR_MORE_DATA) {
				continue
			}
			return nil, err
		}
		count := *(*uint32)(unsafe.Pointer(&buffer[4]))
		capacity := (len(buffer) - 8) / int(unsafe.Sizeof(uintptr(0)))
		if int(count) > capacity {
			continue
		}
		result := make([]uint32, 0, count)
		for index := 0; index < int(count); index++ {
			offset := 8 + index*int(unsafe.Sizeof(uintptr(0)))
			var value uint64
			if unsafe.Sizeof(uintptr(0)) == 8 {
				value = binary.LittleEndian.Uint64(buffer[offset : offset+8])
			} else {
				value = uint64(binary.LittleEndian.Uint32(buffer[offset : offset+4]))
			}
			result = append(result, uint32(value))
		}
		return result, nil
	}
	return nil, errors.New("Windows Job process list is too large")
}

func windowsTargetCommand(path string, args []string) (*exec.Cmd, error) {
	extension := strings.ToLower(filepath.Ext(path))
	if extension != ".cmd" && extension != ".bat" {
		return exec.Command(path, args...), nil
	}
	for _, arg := range args {
		if strings.ContainsAny(arg, "\r\n") {
			return nil, errors.New("CMD/BAT launchers do not support arguments containing newlines; use a native executable launcher")
		}
	}
	comspec := strings.TrimSpace(os.Getenv("COMSPEC"))
	if comspec == "" {
		comspec = "cmd.exe"
	}
	// A batch entrypoint parses the command once in our cmd.exe invocation and
	// commonly parses forwarded %* arguments again inside the script.
	doubleEscape := true
	parts := []string{escapeCmdCommand(filepath.Clean(path))}
	for _, arg := range args {
		parts = append(parts, escapeCmdArgument(arg, doubleEscape))
	}
	shellCommand := strings.Join(parts, " ")
	raw := syscall.EscapeArg(comspec) + ` /d /s /c "` + shellCommand + `"`
	cmd := exec.Command(comspec)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: raw}
	return cmd, nil
}

func escapeCmdCommand(value string) string {
	return escapeCmdMeta(value)
}

func escapeCmdArgument(value string, doubleEscape bool) string {
	var quoted strings.Builder
	quoted.WriteByte('"')
	for index := 0; index < len(value); {
		backslashes := 0
		for index < len(value) && value[index] == '\\' {
			backslashes++
			index++
		}
		if index == len(value) {
			quoted.WriteString(strings.Repeat(`\`, backslashes*2))
			break
		}
		if value[index] == '"' {
			quoted.WriteString(strings.Repeat(`\`, backslashes*2+1))
			quoted.WriteByte('"')
		} else {
			quoted.WriteString(strings.Repeat(`\`, backslashes))
			quoted.WriteByte(value[index])
		}
		index++
	}
	quoted.WriteByte('"')
	result := escapeCmdMeta(quoted.String())
	if doubleEscape {
		result = escapeCmdMeta(result)
	}
	return result
}

func escapeCmdMeta(value string) string {
	const meta = `()[]%!^"` + "`" + `<>&|;, *?`
	var result strings.Builder
	result.Grow(len(value) + 8)
	for _, char := range value {
		if strings.ContainsRune(meta, char) {
			result.WriteByte('^')
		}
		result.WriteRune(char)
	}
	return result.String()
}
