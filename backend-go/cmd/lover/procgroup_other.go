//go:build !windows

package main

// The POSIX half of the process-group contract: Setpgid makes the child a group
// leader, and a negative PID addresses the whole group.

import (
	"os/exec"
	"strings"
	"syscall"
)

// setProcessGroup makes cmd the leader of a new process group, so that a signal
// sent to -PID reaches every process it started.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// hideWindow is a Windows concern; nothing to do where there is no console.
func hideWindow(cmd *exec.Cmd) {}

// sendGroupSignal asks the group to stop.
func sendGroupSignal(pid int, sig syscall.Signal) {
	_ = syscall.Kill(-pid, sig)
}

// killGroupTree ends the group and everything it started.
func killGroupTree(cmd *exec.Cmd) error {
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// syscallKillGroup signals the group a PID leads.
func syscallKillGroup(pid int, sig syscall.Signal) error {
	return syscall.Kill(-pid, sig)
}

const (
	syscallSIGINT  = syscall.SIGINT
	syscallSIGKILL = syscall.SIGKILL
)

// portHolderPID answers "who is listening on this port", from ss or lsof.
//
// Both are tried because neither is guaranteed present: ss ships with iproute2
// and lsof does not, and a machine with one and not the other is ordinary. An
// empty answer means "could not be determined", which the caller says out loud
// rather than hiding.
func portHolderPID(port string) string {
	if out, err := exec.Command("ss", "-lptn", "sport = :"+port).CombinedOutput(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if index := strings.Index(line, "pid="); index >= 0 {
				fields := strings.FieldsFunc(line[index:], func(r rune) bool { return r == ',' || r == ' ' })
				for _, field := range fields {
					if strings.HasPrefix(field, "pid=") {
						return strings.TrimPrefix(field, "pid=")
					}
				}
			}
		}
	}
	if out, err := exec.Command("lsof", "-t", "-i", "TCP:"+port, "-sTCP:LISTEN").CombinedOutput(); err == nil {
		if pid := strings.TrimSpace(string(out)); pid != "" {
			return strings.SplitN(pid, "\n", 2)[0]
		}
	}
	return ""
}
