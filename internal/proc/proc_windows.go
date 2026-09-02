//go:build windows

package proc

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// Windows has no SIGTERM, so Terminate ends the process at once, like Kill.
func Terminate(pid int) error {
	return Kill(pid)
}

func Kill(pid int) error {
	handle, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}

	defer func() { _ = syscall.CloseHandle(handle) }()

	return syscall.TerminateProcess(handle, 1)
}

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

func IsServer(pid int) bool {
	output, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH", "/FO", "CSV").Output()
	if err != nil {
		return false
	}

	name := strings.ToLower(string(output))

	return strings.Contains(name, "mysqld.exe") || strings.Contains(name, "mariadbd.exe")
}
