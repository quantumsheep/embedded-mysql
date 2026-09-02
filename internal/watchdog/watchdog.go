package watchdog

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"

	"github.com/quantumsheep/embedded-mysql/internal/proc"
)

const watchedPidEnvironmentVariable = "EMBEDDED_MYSQL_WATCHDOG_PID"

// A re-executed copy of the current binary becomes the watchdog here, before its main function runs. The copy blocks on its stdin pipe. When the parent dies, in every death mode, the kernel closes the pipe, and the copy signals the watched process at once.
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
	_ = proc.Terminate(pid)
	os.Exit(0)
}

type Watchdog struct {
	cmd *exec.Cmd
	// A garbage-collected write end closes the pipe and fires the watchdog early, so this reference must live as long as the watchdog.
	pipe *os.File
}

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

// Call Stop before the watched process stops. A watchdog that outlives the watched process can send its signal to a recycled pid.
func (w *Watchdog) Stop() {
	_ = w.cmd.Process.Kill()
	_ = w.cmd.Wait()
	_ = w.pipe.Close()
}
