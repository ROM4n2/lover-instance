package main

// The supervisor: this command's child processes, the process groups they lead,
// and the two questions that decide whether a run can start at all — is the port
// free, and did the service become healthy.
//
// The whole point of this file is that a child outlives its parent only if
// somebody let it. `exec.CommandContext` on its own kills the direct child when
// the context is cancelled, which is the entire tree for `python -m uvicorn`
// and is not the entire tree for anything that spawned a process of its own. So
// the child is put in its own process group and the group is what gets signalled:
// one call, whatever the shape of the tree underneath.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os/exec"
	"time"

	"lover-instance/backend-go/internal/nlp"
)

// serviceReadyTimeout is the plan's number: how long a freshly started service
// has to answer /health with "ready". 60s covers a cold import of fastapi plus
// the lexicon build on a slow disk, and it is short enough that a wedged service
// does not look like a hung command. A variable so a test can ask the question in
// milliseconds.
var serviceReadyTimeout = 60 * time.Second

const (
	// healthPollInterval is how often readiness is asked about. Fast enough that
	// a service which becomes ready in 200ms is not waited on for a second, slow
	// enough that the poll is not the expensive part of a start-up.
	healthPollInterval = 250 * time.Millisecond

	// stopGrace is how long a politely signalled process group gets to exit
	// before the tree-kill. It matches cmd/server's own shutdown timeout, so the
	// graceful path is not cut short by the force that follows it.
	stopGrace = 5 * time.Second

	// forceStopTimeout bounds the wait after the tree-kill. A process that
	// survives SIGKILL is a kernel problem, and Ctrl-C must not become a hang.
	forceStopTimeout = 5 * time.Second
)

// buildNLPCommand is the one seam a test replaces to hand the supervisor a fake
// service process. Production builds the uvicorn invocation the service
// documents; a test builds something that sleeps.
var buildNLPCommand = func(ctx context.Context, python, port string) *exec.Cmd {
	return exec.CommandContext(ctx, python, "-m", "uvicorn", "app.main:app", "--port", port)
}

// serviceConfig is what the supervisor needs to start the NLP service.
type serviceConfig struct {
	python string
	port   string
	dir    string // the nlp-service/ directory uvicorn must run from
	stderr io.Writer
}

// child is a subprocess and the process group it leads. done is closed once the
// process has been reaped, which is the only safe way to read waitErr.
type child struct {
	cmd     *exec.Cmd
	done    chan struct{}
	waitErr error

	// cleanup is what to do with what the process was using, once the process
	// is gone. A caller that compiled a binary into the temporary directory
	// owns deleting it, and a running executable is a file the operating
	// system still holds open — so this runs after the wait, never before.
	cleanup func()
}

// newChild starts cmd as the leader of its own process group, with its output
// going to stderr so the service's own startup lines are visible in the same
// terminal as this command's.
func newChild(ctx context.Context, cmd *exec.Cmd, dir string, stderr io.Writer) (*child, error) {
	cmd.Dir = dir
	cmd.Stdout = stderr
	cmd.Stderr = stderr
	setProcessGroup(cmd)
	// CommandContext's default Cancel kills the direct child, which is not the
	// guarantee this command makes; withCancelKillsTheGroup replaces it.
	withCancelKillsTheGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", cmd.Path, err)
	}
	c := &child{cmd: cmd, done: make(chan struct{})}
	go func() {
		c.waitErr = cmd.Wait()
		close(c.done)
	}()
	return c, nil
}

// startService starts the NLP service and returns the child leading it.
func startService(ctx context.Context, cfg serviceConfig) (*child, error) {
	return newChild(ctx, buildNLPCommand(ctx, cfg.python, cfg.port), cfg.dir, cfg.stderr)
}

// exited reports whether the process has been reaped. Only after it returns true
// may waitErr be read.
func (c *child) exited() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// wait blocks until the process has been reaped.
func (c *child) wait() { <-c.done }

// stop ends the child politely and then certainly.
//
// The polite signal goes to the process group, not the process, because a
// service that spawned a worker is the case this exists for: uvicorn on Windows
// runs in-process today, and a future version that does not must not leave the
// worker behind. The force that follows is what makes the guarantee hold for a
// process that ignored the polite signal.
func (c *child) stop() {
	if c.cmd.Process == nil {
		return
	}
	signalGroup(c.cmd)
	select {
	case <-c.done:
		return
	case <-time.After(stopGrace):
	}
	_ = killGroup(c.cmd)
	select {
	case <-c.done:
	case <-time.After(forceStopTimeout):
	}
}

// runCleanup runs what was registered for after the process is gone.
//
// Whether it succeeded is the cleanup's own business and never this command's
// exit code: a leftover file in the temporary directory is untidy, while
// reporting it would turn a run that did its job into a run that claims to have
// failed.
func (c *child) runCleanup() {
	if c.cleanup != nil {
		c.cleanup()
	}
}

// waitReady polls /health until the service says it can serve, the context is
// cancelled, or the timeout expires. Each of the three is a different failure
// with a different message, and the caller turns them into different exit codes.
func (c *child) waitReady(ctx context.Context, baseURL string, timeout time.Duration) error {
	client, err := nlp.NewClient(baseURL)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	for {
		// Checked before the poll: a service that died on an import error would
		// otherwise be waited on for the whole timeout to say nothing at all.
		if c.exited() {
			return fmt.Errorf("the NLP service process exited before it became ready (%v); its output is above, and starting it by hand with `cd nlp-service && python -m uvicorn app.main:app --port %s` shows the same error", c.waitErr, portOf(baseURL))
		}
		health, err := client.Health(ctx)
		if err == nil && health.Status == "ready" {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the NLP service did not report ready within %s (waited %s); its output is above — start it by hand with `cd nlp-service && python -m uvicorn app.main:app --port %s` to see what it is stuck on", timeout, timeout, portOf(baseURL))
		}
		select {
		case <-time.After(healthPollInterval):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// portError marks "the port is taken". It is a type rather than a string so the
// caller can classify it into exitPort without matching on prose.
type portError struct{ err error }

func (e *portError) Error() string { return e.err.Error() }
func (e *portError) Unwrap() error { return e.err }

// ensurePortFree binds the port long enough to prove nobody else holds it, then
// releases it. Binding rather than connecting is what makes this a real check: a
// stale service that accepts connections but never answers would pass a dial.
//
// purpose names what needs the port, because the same check serves the NLP
// service and the dashboard server and a message that said "the NLP service" for
// both would send a reader to the wrong log.
//
// The occupier's PID is reported when it can be found, and saying so plainly when
// it cannot is part of the contract: on Windows, reading another process's port
// owner needs an elevated netstat, and a message that quietly omitted the PID
// would leave the reader wondering whether it was looked for.
func ensurePortFree(purpose, port string) error { return checkPortFree(purpose, port) }

// checkPortFree is the real implementation behind the seam. It is a variable so a
// test can prove the empty argument list reaches the dashboard path without
// depending on whether the machine happens to have the default port free: the
// port is a property of this machine, and a test that asserted on it would fail
// or pass for a reason unrelated to the code under test.
var checkPortFree = func(purpose, port string) error {
	listener, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err == nil {
		_ = listener.Close()
		return nil
	}
	if holder := portHolderPID(port); holder != "" {
		return &portError{fmt.Errorf("%s needs port %s, but PID %s is already listening on it; stop that process, or point the service at one that is already running", purpose, port, holder)}
	}
	return &portError{fmt.Errorf("%s needs port %s, but this command could not listen on it (%v), so the port is probably already in use; the PID holding it could not be determined — on Windows run `netstat -ano | findstr :%s` as administrator to see it", purpose, port, err, port)}
}

// nlpPort reads the port out of a service URL. It is a function of the URL rather
// than a constant, so the port this command keeps free is the port the shared
// layer defaults to and the two cannot drift.
func nlpPort(baseURL string) string {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return parsed.Port()
}

// portOf is the port half of a URL, for a message that has to name it.
func portOf(baseURL string) string {
	if port := nlpPort(baseURL); port != "" {
		return port
	}
	return baseURL
}

// asPortError classifies an error from ensurePortFree.
func asPortError(err error) bool {
	var target *portError
	return errors.As(err, &target)
}

// signalGroup asks the group to shut down. Errors are ignored on purpose: the
// force that follows is what guarantees the outcome, and a signal that could not
// be delivered is not worth reporting when the answer is already on its way.
func signalGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	sendGroupSignal(cmd.Process.Pid, syscallSIGINT)
}

// killGroup ends the whole group, now. This is what cmd.Cancel runs, so it must
// be safe to call from Go's own watchdog goroutine and safe to call twice.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := killGroupTree(cmd); err != nil {
		// The direct child is the one thing this process can always reach.
		return cmd.Process.Kill()
	}
	return nil
}

// withCancelKillsTheGroup replaces CommandContext's default Cancel, which kills
// the direct child only. The group is the unit that must die, so the group is
// what gets signalled; a hard kill rather than a graceful one, because a
// cancelled run is over and the service holds no state worth flushing (the job
// row is closed by app.Run on a context that ignores cancellation).
func withCancelKillsTheGroup(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return killGroup(cmd) }
	// Without a WaitDelay, a process that ignored the kill would leave Wait
	// blocked forever and Ctrl-C would never return.
	cmd.WaitDelay = forceStopTimeout
}
