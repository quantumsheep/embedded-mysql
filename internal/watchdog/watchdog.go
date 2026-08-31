// Package watchdog stops an orphan child process when the current process dies. The watchdog is a re-executed copy of the current binary, so it needs no shell.
package watchdog

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

const watchedPidEnvironmentVariable = "EMBEDDED_MYSQL_WATCHDOG_PID"

// init turns a re-executed copy of the current binary into the watchdog before its main function runs. The copy blocks on its stdin pipe. When the parent dies, in every death mode, the kernel closes the pipe, and the copy signals the watched process at once.
func init() {
	pidText := os.Getenv(watchedPidEnvironmentVariable)
	if pidText == "" {
		return
	}

	pid, err := strconv.Atoi(pidText)
	if err != nil {
		os.Exit(1)
	}

	_, _ = io.Copy(io.Discard, os.Stdin)
	_ = syscall.Kill(pid, syscall.SIGTERM)
	os.Exit(0)
}

// Watchdog is one process that watches the current process.
type Watchdog struct {
	cmd *exec.Cmd
	// pipe stays referenced here for the life of the watchdog. A garbage-collected write end closes the pipe and fires the watchdog early.
	pipe *os.File
}

// Start runs a watchdog for the process with the given pid.
func Start(pid int) (*Watchdog, error) {
	executablePath, err := os.Executable()
	if err != nil {
		return nil, err
	}

	pipeRead, pipeWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(executablePath)
	cmd.Stdin = pipeRead
	cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%d", watchedPidEnvironmentVariable, pid))

	err = cmd.Start()
	_ = pipeRead.Close()

	if err != nil {
		_ = pipeWrite.Close()

		return nil, err
	}

	return &Watchdog{
		cmd:  cmd,
		pipe: pipeWrite,
	}, nil
}

// Stop kills the watchdog process and waits for it. Call Stop before the watched process stops. A watchdog that outlives the watched process can send its signal to a recycled pid.
func (w *Watchdog) Stop() {
	_ = w.cmd.Process.Kill()
	_ = w.cmd.Wait()
	_ = w.pipe.Close()
}
