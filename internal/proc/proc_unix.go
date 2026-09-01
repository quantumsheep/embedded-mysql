//go:build !windows

// Package proc signals and inspects processes by pid on every supported platform.
package proc

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// Terminate asks the process to shut down.
func Terminate(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

// Kill force-stops the process.
func Kill(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}

// Alive reports whether a process with the given pid exists.
func Alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// IsServer reports whether the process with the given pid runs mysqld or mariadbd.
func IsServer(pid int) bool {
	output, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}

	name := strings.TrimSpace(string(output))

	return strings.HasSuffix(name, "mysqld") || strings.HasSuffix(name, "mariadbd")
}
