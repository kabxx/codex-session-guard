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
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
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
	job, err := createWindowsJob()
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
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	if err := cmd.Start(); err != nil {
		return processResult{Err: err, ExitCode: 1}
	}
	if err := assignProcessToJob(job, cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return processResult{Err: fmt.Errorf("failed to assign the launcher to the Windows Job: %w", err), ExitCode: 1}
	}
	started, _ := processIdentity(cmd.Process.Pid)
	onStart(cmd.Process.Pid, started)
	if err := resumeSuspendedProcess(cmd.Process.Pid); err != nil {
		_ = windows.TerminateJobObject(job, 1)
		_ = cmd.Wait()
		return processResult{Err: fmt.Errorf("failed to resume the supervised launcher: %w", err), ExitCode: 1}
	}

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

func createWindowsJob() (windows.Handle, error) {
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
	return job, nil
}

func assignProcessToJob(job windows.Handle, pid int) error {
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(pid),
	)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	return windows.AssignProcessToJobObject(job, process)
}

func resumeSuspendedProcess(pid int) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		return err
	}
	for {
		if entry.OwnerProcessID == uint32(pid) {
			thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if openErr != nil {
				return openErr
			}
			_, resumeErr := windows.ResumeThread(thread)
			_ = windows.CloseHandle(thread)
			return resumeErr
		}
		entry.Size = uint32(unsafe.Sizeof(windows.ThreadEntry32{}))
		if err := windows.Thread32Next(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				return errors.New("the suspended launcher thread was not found")
			}
			return err
		}
	}
}

func waitForWindowsJob(job windows.Handle, runID string, forceStop bool) (bool, bool) {
	active, err := activeJobProcesses(job)
	if err != nil || active == 0 {
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
		if err != nil || active == 0 {
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
