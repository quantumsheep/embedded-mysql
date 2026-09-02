//go:build !windows

package proc

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func Terminate(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

func Kill(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}

func Alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func IsServer(pid int) bool {
	output, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}

	name := strings.TrimSpace(string(output))

	return strings.HasSuffix(name, "mysqld") || strings.HasSuffix(name, "mariadbd")
}
