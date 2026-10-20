//go:build windows

package main

// The Windows half of the process-group contract.
//
// CREATE_NEW_PROCESS_GROUP puts the child in a group of its own, which is what
// makes both signals below addressable: CTRL_BREAK_EVENT goes to a process
// group, and `taskkill /T` walks a parent-child tree. Without the flag the child
// shares this process's group, and a Ctrl-C meant for us would take the service
// down with it — which sounds harmless until the service is a dashboard holding
// a database connection.
//
// A Job Object is the other way to do this, and it is the stronger one: a job
// survives the death of the process that created it, so an orphan is impossible
// by construction rather than by a kill that has to arrive in time. It was not
// chosen because it needs a kernel handle per child plus a completion port to
// notice the child's exit, and the group flag plus taskkill covers the tree this
// command actually creates today. Recorded here so the next person does not have
// to rediscover the trade.

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// CREATE_NEW_PROCESS_GROUP. Defined here rather than imported because the
// standard library's syscall package does not name it, and golang.org/x/sys is
// not a dependency of this module.
const createNewProcessGroup = 0x00000200

// CTRL_BREAK_EVENT. CTRL_C_EVENT is deliberately not used: it cannot be
// delivered to a single group without also reaching this process's own group.
const ctrlBreakEvent = 1

var generateConsoleCtrlEvent = syscall.NewLazyDLL("kernel32.dll").NewProc("GenerateConsoleCtrlEvent")

// setProcessGroup makes cmd the leader of a new process group.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup,
		HideWindow:    true,
	}
}

// hideWindow keeps a helper process (taskkill) from flashing a console window
// over the terminal this command is running in.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

// sendConsoleCtrlEvent delivers CTRL_BREAK to one process group. It fails
// silently when the target does not share this process's console — a service
// started from a terminal that has since closed, say — which is exactly the case
// the force kill that follows exists for.
func sendConsoleCtrlEvent(event uint32, pid int) {
	_, _, _ = generateConsoleCtrlEvent.Call(uintptr(event), uintptr(pid))
}

// sendGroupSignal asks the group to stop. On Windows that is a console control
// event, and Go's runtime maps CTRL_BREAK to SIGINT, so a Go service (and
// uvicorn, which is Python but installs the same handler) shuts down on it.
func sendGroupSignal(pid int, _ int) {
	sendConsoleCtrlEvent(ctrlBreakEvent, pid)
}

// killGroupTree ends the group and everything it started.
//
// /T walks the tree, /F is required because a process that ignored the polite
// signal will not answer a polite request either. The direct child is killed
// afterwards as well: taskkill reports a failure for an already-dead parent,
// which would otherwise leave a tree whose root had exited on its own.
func killGroupTree(cmd *exec.Cmd) error {
	taskkill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	hideWindow(taskkill)
	_ = taskkill.Run()
	return cmd.Process.Kill()
}

// syscallKillGroup is not how a group is addressed on Windows: there is no
// negative PID here, the group is reached with a console control event and the
// tree with taskkill. It exists so the supervisor can name one function on every
// platform, and the platform branches never reach it.
func syscallKillGroup(int, int) error { return nil }

const (
	syscallSIGINT  = 0
	syscallSIGKILL = 0
)

// portHolderPID answers "who is listening on this port", from netstat.
//
// The local-address column is matched rather than the whole line, and only on a
// LISTENING row, because an ESTABLISHED connection to a remote port would
// otherwise be reported as this machine holding the port. An empty answer means
// "could not be determined", which the caller says out loud rather than hiding.
func portHolderPID(port string) string {
	out, err := exec.Command("netstat", "-ano", "-p", "tcp").CombinedOutput()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "TCP" || fields[3] != "LISTENING" {
			continue
		}
		if !strings.HasSuffix(fields[1], ":"+port) {
			continue
		}
		return fields[4]
	}
	return ""
}
