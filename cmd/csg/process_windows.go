//go:build windows

package main

import (
	"os"
	"strings"
	"syscall"
	"unsafe"
)

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259
	errorInvalidParameter          = syscall.Errno(87)
	th32csSnapProcess              = 0x00000002
)

var (
	kernel32                   = syscall.NewLazyDLL("kernel32.dll")
	procOpenProcess            = kernel32.NewProc("OpenProcess")
	procGetProcessTimes        = kernel32.NewProc("GetProcessTimes")
	procGetExitCode            = kernel32.NewProc("GetExitCodeProcess")
	procCloseHandle            = kernel32.NewProc("CloseHandle")
	procCreateToolhelpSnapshot = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32First         = kernel32.NewProc("Process32FirstW")
	procProcess32Next          = kernel32.NewProc("Process32NextW")
)

type processEntry32 struct {
	Size            uint32
	Usage           uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	Threads         uint32
	ParentProcessID uint32
	PriClassBase    int32
	Flags           uint32
	ExeFile         [syscall.MAX_PATH]uint16
}

func processIdentity(pid int) (uint64, bool) {
	started, state := queryWindowsProcess(pid)
	return started, state == processAlive
}

func processLiveness(pid int, expectedStart uint64) processState {
	started, state := queryWindowsProcess(pid)
	if state == processAlive && expectedStart != 0 && started != expectedStart {
		return processDead
	}
	return state
}

func queryWindowsProcess(pid int) (uint64, processState) {
	if pid <= 0 {
		return 0, processDead
	}
	handle, _, callErr := procOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(uint32(pid)))
	if handle == 0 {
		if errno, ok := callErr.(syscall.Errno); ok && errno == errorInvalidParameter {
			return 0, processDead
		}
		return 0, processUnknown
	}
	defer procCloseHandle.Call(handle)

	var created, exited, kernel, user syscall.Filetime
	ok, _, _ := procGetProcessTimes.Call(
		handle,
		uintptr(unsafe.Pointer(&created)),
		uintptr(unsafe.Pointer(&exited)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if ok == 0 {
		return 0, processUnknown
	}
	var exitCode uint32
	ok, _, _ = procGetExitCode.Call(handle, uintptr(unsafe.Pointer(&exitCode)))
	if ok == 0 {
		return 0, processUnknown
	}
	if exitCode != stillActive {
		return 0, processDead
	}
	stamp := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	return stamp, processAlive
}

// nearestCodexAncestor covers the tiny interval between CreateProcess
// returning in the wrapper and the wrapper persisting the child PID.
func nearestCodexAncestor() (int, uint64, bool) {
	entries := processSnapshot()
	pid := os.Getppid()
	for depth := 0; depth < 12 && pid > 0; depth++ {
		entry, ok := entries[uint32(pid)]
		if !ok {
			return 0, 0, false
		}
		if strings.EqualFold(syscall.UTF16ToString(entry.ExeFile[:]), "codex.exe") {
			started, alive := processIdentity(pid)
			return pid, started, alive
		}
		pid = int(entry.ParentProcessID)
	}
	return 0, 0, false
}

func processSnapshot() map[uint32]processEntry32 {
	result := make(map[uint32]processEntry32)
	handle, _, _ := procCreateToolhelpSnapshot.Call(th32csSnapProcess, 0)
	if handle == 0 || handle == ^uintptr(0) {
		return result
	}
	defer procCloseHandle.Call(handle)

	var entry processEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	ok, _, _ := procProcess32First.Call(handle, uintptr(unsafe.Pointer(&entry)))
	for ok != 0 {
		result[entry.ProcessID] = entry
		entry.Size = uint32(unsafe.Sizeof(entry))
		ok, _, _ = procProcess32Next.Call(handle, uintptr(unsafe.Pointer(&entry)))
	}
	return result
}
