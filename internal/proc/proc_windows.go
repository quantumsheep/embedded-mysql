//go:build windows

// Package proc signals and inspects processes by pid on every supported platform.
package proc

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// Terminate stops the process. Windows has no SIGTERM, so Terminate ends the process at once, like Kill.
func Terminate(pid int) error {
	return Kill(pid)
}

// Kill force-stops the process.
func Kill(pid int) error {
	handle, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}

	defer func() { _ = syscall.CloseHandle(handle) }()

	return syscall.TerminateProcess(handle, 1)
}

// Alive reports whether a process with the given pid exists.
func Alive(pid int) bool {
	// STILL_ACTIVE, the exit code of a process that has not exited.
	const stillActive = 259

	// PROCESS_QUERY_LIMITED_INFORMATION, which package syscall does not define.
	const processQueryLimitedInformation = 0x1000

	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}

	defer func() { _ = syscall.CloseHandle(handle) }()

	var exitCode uint32

	err = syscall.GetExitCodeProcess(handle, &exitCode)

	return err == nil && exitCode == stillActive
}

// IsServer reports whether the process with the given pid runs mysqld or mariadbd.
func IsServer(pid int) bool {
	output, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH", "/FO", "CSV").Output()
	if err != nil {
		return false
	}

	name := strings.ToLower(string(output))

	return strings.Contains(name, "mysqld.exe") || strings.Contains(name, "mariadbd.exe")
}
