package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// Why this file exists
//
// A tunnel provider is a long-lived child process, and this server used to treat
// it as a fire-and-forget one. Two defects followed from that, and both of them
// reached the operator as "the server died, I restarted it, it was fine".
//
//  1. A provider's stdout and stderr were read only until the public URL
//     appeared, and then never again. A pipe whose reader has walked away fills
//     up after a few kilobytes, and the writer's next write blocks in the kernel
//     with no timeout and no way out. In steady state these agents log almost
//     nothing, which is why the failure was rare. A changed IP address is exactly
//     the event that produces a burst of reconnect logging, so that is when the
//     remaining buffer was spent and the agent wedged mid-write, unable to run
//     the reconnect logic it already has. Nothing recovered, because nothing was
//     ever going to read that pipe again.
//
//  2. Nothing called Wait, so nothing noticed an exit either. When a provider
//     died outright, the server kept running and kept advertising a URL that no
//     longer routed anywhere, while /healthz kept answering ok because it only
//     proved the local listener was alive.
//
// So drainOutput never stops reading until the stream ends, and every provider is
// supervised by a goroutine that waits for the exit, says so, and starts a
// replacement with a backoff.

// maxTunnelLineBytes caps how much of a single output line is kept. A line this
// long is a provider dumping a stack trace or a base64 blob; the reason to read
// it is to keep the pipe moving, not to store it.
const maxTunnelLineBytes = 8 * 1024

// tunnelReadBuffer is the read buffer used for a provider's output.
const tunnelReadBuffer = 32 * 1024

// defaultRestartBase is the pause before the first restart. Most tunnel exits
// are a dropped connection that reconnects on the next dial, so the first retry
// is quick.
const defaultRestartBase = 2 * time.Second

// defaultRestartMax is the ceiling on the backoff, so a provider that is down
// for a real outage does not restart in a tight loop for hours.
const defaultRestartMax = 2 * time.Minute

// The states a tunnel can be reported in. "down" means the restart limit was
// reached, which is different from "stopped": one gave up, the other was asked.
const (
	tunnelOff        = "off"
	tunnelUp         = "up"
	tunnelRestarting = "restarting"
	tunnelStopped    = "stopped"
	tunnelDown       = "down"
)

// TunnelStatus is everything the server can say about its tunnel without being
// asked twice: what is published, whether it is up, and if it is not, what the
// provider said on its way out.
//
// It is reported by /healthz and by the server_status tool, because "is the
// tunnel up" and "is the local listener up" are different questions and the
// second one used to be the only one anybody could ask.
type TunnelStatus struct {
	State       string   `json:"state" jsonschema:"off when no tunnel is configured, up while a provider is publishing, restarting between attempts, down once restarts were given up on, stopped after a deliberate shutdown."`
	Provider    string   `json:"provider,omitempty" jsonschema:"Which provider is publishing this server: cloudflared, ngrok or pinggy."`
	Detail      string   `json:"detail,omitempty" jsonschema:"Which flavour of that provider, such as a named tunnel, a quick tunnel or a reserved domain."`
	URL         string   `json:"url,omitempty" jsonschema:"URL currently published. A restart can hand back a different URL, in which case a client pinned to the old one has to be repointed."`
	PID         int      `json:"pid,omitempty" jsonschema:"Process id of the running provider, for matching it against the process list."`
	Uptime      string   `json:"uptime,omitempty" jsonschema:"How long the current provider process has been running, which resets on every restart."`
	Restarts    int      `json:"restarts" jsonschema:"How many times the provider has been replaced since this server started. Anything above zero means the published URL was unreachable for a while."`
	LastExit    string   `json:"lastExit,omitempty" jsonschema:"Exit status of the most recent provider process to die, which tells a tunnel that fell over apart from a network problem at the caller's end."`
	LastExitAgo string   `json:"lastExitAgo,omitempty" jsonschema:"How long ago that exit happened."`
	Recent      []string `json:"recent,omitempty" jsonschema:"Tail of the provider's own output, oldest first, only when recentLines asked for it. This is usually where the reason for an exit is."`
}

// tunnelSupervisor is the live state of the tunnel: the child, the tail of its
// output, and what has happened to it so far.
type tunnelSupervisor struct {
	mu         sync.Mutex
	child      *tunnelChild
	output     *tunnelLog
	status     TunnelStatus
	lastExitAt time.Time
	stopping   atomic.Bool
	halt       chan struct{}
}

// tunnelSpec describes how to start one provider and how to recognise what it
// prints. Every provider reduces to this, so the draining, supervision and
// restart logic is written once instead of four times.
type tunnelSpec struct {
	provider string
	detail   string
	timeout  time.Duration
	build    func(ctx context.Context) *exec.Cmd
	match    func(line string) string
	fatal    func(line string) string
}

// tunnelChild is one running provider process.
type tunnelChild struct {
	cmd     *exec.Cmd
	ctx     context.Context
	cancel  context.CancelFunc
	drained *sync.WaitGroup
	output  *tunnelLog
	url     string
	started time.Time
}

// tunnelResult reports how a start attempt ended. The three failure modes are
// kept apart because each provider words them differently: err is "it would not
// start", reason is "the agent gave up and said why", and an empty url with
// neither of those is "it never reported a URL in time".
type tunnelResult struct {
	url    string
	reason string
	err    error
	output *tunnelLog
	child  *tunnelChild
}

// drainOutput reads a provider's output until the stream ends, handing each line
// to onLine.
//
// It never returns early, and that is the whole point: returning early is what
// left a full pipe behind and blocked the provider's next write forever. A line
// longer than the read buffer is consumed in pieces and truncated rather than
// buffered without limit, and bufio.Scanner is deliberately not used here
// because it stops with an error on a line over 64 KB, which would abandon the
// pipe again for exactly the pathological output most likely to appear during a
// network failure.
func drainOutput(stream io.Reader, onLine func(string)) {
	reader := bufio.NewReaderSize(stream, tunnelReadBuffer)
	line := make([]byte, 0, 256)
	truncated := false

	for {
		chunk, err := reader.ReadSlice('\n')
		if len(chunk) > 0 {
			if room := maxTunnelLineBytes - len(line); room > 0 {
				if len(chunk) > room {
					line = append(line, chunk[:room]...)
					truncated = true
				} else {
					line = append(line, chunk...)
				}
			} else {
				truncated = true
			}
		}

		// The line is longer than the buffer, so keep reading it: the tail is
		// already capped, and stopping here would stall the writer.
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}

		if err == nil || len(line) > 0 {
			text := strings.TrimRight(string(line), "\r\n")
			if truncated {
				text += fmt.Sprintf(" [line truncated at %d bytes]", maxTunnelLineBytes)
			}
			onLine(text)
			line = line[:0]
			truncated = false
		}

		if err != nil {
			return
		}
	}
}

// runTunnel starts a provider, announces it, and supervises it from then on.
//
// The returned result is for the caller's own error reporting; the caller does
// not need to keep the process, because a successful start is adopted here.
func (s *Server) runTunnel(spec tunnelSpec) tunnelResult {
	res := s.launchChild(spec)
	if res.child == nil {
		return res
	}

	s.adoptChild(spec, res.child, 0)
	printTunnelURL(res.url)
	go s.superviseTunnel(spec, res.child)
	return res
}

// launchChild starts one provider process and waits for it to publish a URL.
func (s *Server) launchChild(spec tunnelSpec) tunnelResult {
	output := newTunnelLog(spec.provider)
	ctx, cancel := context.WithCancel(context.Background())
	cmd := spec.build(ctx)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return tunnelResult{err: err, output: output}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return tunnelResult{err: err, output: output}
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return tunnelResult{err: err, output: output}
	}

	found := make(chan string, 1)
	failed := make(chan string, 1)

	var drained sync.WaitGroup
	drained.Add(2)
	watch := func(stream io.Reader) {
		defer drained.Done()
		drainOutput(stream, func(line string) {
			output.add(line)

			if url := spec.match(line); url != "" {
				select {
				case found <- url:
				default:
				}
				return
			}
			if spec.fatal == nil {
				return
			}
			if reason := spec.fatal(line); reason != "" {
				select {
				case failed <- reason:
				default:
				}
			}
		})
	}
	go watch(stdout)
	go watch(stderr)

	child := &tunnelChild{
		cmd:     cmd,
		ctx:     ctx,
		cancel:  cancel,
		drained: &drained,
		output:  output,
		started: time.Now(),
	}

	select {
	case url := <-found:
		child.url = url
		return tunnelResult{url: url, output: output, child: child}
	case reason := <-failed:
		discardChild(child)
		return tunnelResult{reason: reason, output: output}
	case <-time.After(spec.timeout):
		discardChild(child)
		return tunnelResult{output: output}
	}
}

// discardChild stops a process that never became useful, and still reaps it.
//
// The reap runs in the background because Wait may only be called once every
// read from the pipes has finished, and a wedged provider may take until the
// kill to close them. Skipping it is what used to leak a process handle per
// failed attempt.
func discardChild(child *tunnelChild) {
	child.cancel()
	killProcessTree(child.cmd)

	go func() {
		child.drained.Wait()
		_ = child.cmd.Wait()
	}()
}

// superviseTunnel owns a tunnel for the rest of the server's life: it waits for
// the current child to exit, reports why, and replaces it.
//
// One goroutine covers every restart rather than one per child, so a tunnel that
// flaps for an hour does not leave a goroutine behind for each attempt.
func (s *Server) superviseTunnel(spec tunnelSpec, first *tunnelChild) {
	child := first
	restarts := 0

	for {
		// Wait may only be called once the pipe reads are done, and the reads
		// finish when the process closes its output, so this ordering is what
		// makes the exit observable at all.
		child.drained.Wait()
		err := child.cmd.Wait()

		if s.tunnel.stopping.Load() || child.ctx.Err() != nil {
			s.setTunnelState(tunnelStopped, "")
			return
		}

		reason := describeExit(err)
		s.recordTunnelExit(reason)
		log.Warn().
			Str("provider", spec.provider).
			Str("reason", reason).
			Msg("tunnel exited")
		fmt.Printf("\n  the %s tunnel exited (%s)\n", spec.provider, reason)
		child.output.report()
		killProcessTree(child.cmd)

		replacement := s.restartTunnel(spec, child, &restarts)
		if replacement == nil {
			return
		}
		child = replacement
	}
}

// restartTunnel keeps trying to bring a provider back, and reports the new child
// once one starts. A nil return means it is not coming back: either the server is
// shutting down or the restart limit was reached.
func (s *Server) restartTunnel(spec tunnelSpec, previous *tunnelChild, restarts *int) *tunnelChild {
	for {
		*restarts++
		if limit := s.restartLimit; limit > 0 && *restarts > limit {
			s.setTunnelState(tunnelDown, "")
			fmt.Printf("  giving up on the %s tunnel after %d attempts; restart the server to try again\n",
				spec.provider, limit)
			return nil
		}

		delay := s.restartDelay(*restarts)
		s.setTunnelState(tunnelRestarting, "")
		fmt.Printf("  restarting the %s tunnel in %s (attempt %d)\n",
			spec.provider, delay.Round(time.Millisecond), *restarts)

		if !s.waitBeforeRestart(delay) {
			s.setTunnelState(tunnelStopped, "")
			return nil
		}

		res := s.launchChild(spec)
		if res.child == nil {
			if res.err != nil {
				fmt.Printf("  the %s tunnel would not start: %v\n", spec.provider, res.err)
			} else if res.reason != "" {
				fmt.Printf("  the %s tunnel gave up: %s\n", spec.provider, res.reason)
			} else {
				fmt.Printf("  the %s tunnel reported no URL within %s\n", spec.provider, spec.timeout)
			}
			continue
		}

		s.adoptChild(spec, res.child, *restarts)

		// A quick tunnel hands out a new hostname every session, so a client
		// pinned to the old one is now pointing at nothing. Saying so is the
		// difference between a recovered tunnel and a recovered tunnel nobody
		// can reach.
		if res.child.url == previous.url {
			fmt.Printf("  the %s tunnel is back on the same URL: %s\n", spec.provider, res.child.url)
		} else {
			fmt.Printf("  the %s tunnel came back on a new URL, so a client pinned to %s must be repointed\n",
				spec.provider, previous.url)
			printTunnelURL(res.child.url)
		}
		return res.child
	}
}

// waitBeforeRestart pauses before the next attempt, and reports false if the
// server was asked to stop while waiting.
func (s *Server) waitBeforeRestart(delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-timer.C:
		return !s.tunnel.stopping.Load()
	case <-s.tunnel.halted():
		return false
	}
}

// restartDelay is the pause before restart attempt n, counting from 1. The
// doubling is deterministic: this is one server on one machine, so there is no
// thundering herd to spread out, and a predictable delay is testable.
func (s *Server) restartDelay(attempt int) time.Duration {
	delay := s.restartBase
	if delay <= 0 {
		delay = defaultRestartBase
	}
	ceiling := s.restartMax
	if ceiling <= 0 {
		ceiling = defaultRestartMax
	}

	for i := 1; i < attempt && delay < ceiling; i++ {
		delay *= 2
	}
	if delay > ceiling {
		delay = ceiling
	}
	return delay
}

// adoptChild records a started provider as the live one.
func (s *Server) adoptChild(spec tunnelSpec, child *tunnelChild, restarts int) {
	s.tunnel.mu.Lock()
	s.tunnel.child = child
	s.tunnel.output = child.output
	s.tunnel.status.State = tunnelUp
	s.tunnel.status.Provider = spec.provider
	s.tunnel.status.Detail = spec.detail
	s.tunnel.status.URL = child.url
	s.tunnel.status.Restarts = restarts
	s.tunnel.status.PID = 0
	if child.cmd.Process != nil {
		s.tunnel.status.PID = child.cmd.Process.Pid
	}
	s.tunnel.mu.Unlock()
}

// stopTunnel shuts the tunnel down for good, and is what the signal handler
// calls. It also tells the supervisor not to start a replacement, which is the
// difference between a deliberate stop and a crash.
func (s *Server) stopTunnel() {
	s.tunnel.stopping.Store(true)

	s.tunnel.mu.Lock()
	if s.tunnel.halt == nil {
		s.tunnel.halt = make(chan struct{})
	}
	select {
	case <-s.tunnel.halt:
	default:
		close(s.tunnel.halt)
	}
	child := s.tunnel.child
	s.tunnel.child = nil
	s.tunnel.status.State = tunnelStopped
	s.tunnel.status.PID = 0
	s.tunnel.mu.Unlock()

	if child == nil {
		return
	}
	child.cancel()
	killProcessTree(child.cmd)
}

// halted reports the channel closed when the tunnel is asked to stop.
func (t *tunnelSupervisor) halted() <-chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.halt == nil {
		t.halt = make(chan struct{})
	}
	return t.halt
}

func (s *Server) setTunnelState(state, reason string) {
	s.tunnel.mu.Lock()
	defer s.tunnel.mu.Unlock()
	s.tunnel.status.State = state
	if reason != "" {
		s.tunnel.status.LastExit = reason
	}
}

func (s *Server) recordTunnelExit(reason string) {
	s.tunnel.mu.Lock()
	defer s.tunnel.mu.Unlock()
	s.tunnel.status.LastExit = reason
	s.tunnel.status.PID = 0
	s.tunnel.lastExitAt = time.Now()
}

// TunnelReport is a snapshot of the tunnel, with the last recentLines of
// provider output when any were asked for.
//
// The output tail survives the child that produced it, because the moment the
// tail matters most is just after the process it came from has gone.
func (s *Server) TunnelReport(recentLines int) TunnelStatus {
	s.tunnel.mu.Lock()
	status := s.tunnel.status
	child := s.tunnel.child
	output := s.tunnel.output
	lastExitAt := s.tunnel.lastExitAt
	s.tunnel.mu.Unlock()

	if status.State == "" {
		status.State = tunnelOff
	}
	if child != nil {
		status.Uptime = time.Since(child.started).Round(time.Second).String()
	}
	if !lastExitAt.IsZero() {
		status.LastExitAgo = time.Since(lastExitAt).Round(time.Second).String() + " ago"
	}
	if recentLines > 0 && output != nil {
		status.Recent = output.tail(recentLines)
	}
	return status
}

// describeExit turns a Wait error into something worth printing.
func describeExit(err error) string {
	if err == nil {
		return "exit status 0"
	}

	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Sprintf("exit status %d", exit.ExitCode())
	}
	return err.Error()
}

// killProcessTree kills a provider and anything it started.
//
// exec.CommandContext cancellation kills the direct child only. cloudflared and
// ssh both start helpers, and on Windows Process.Kill does not walk the tree, so
// a stale agent could outlive a shutdown still holding the tunnel and make the
// next start fail as a duplicate connection. taskkill /T walks it.
func killProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid

	if runtime.GOOS != "windows" {
		_ = cmd.Process.Kill()
		return
	}

	// An already-dead pid makes taskkill fail, which is the common case here
	// and not worth reporting above debug.
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	if out, err := kill.CombinedOutput(); err != nil {
		log.Debug().
			Err(err).
			Int("pid", pid).
			Str("output", strings.TrimSpace(string(out))).
			Msg("taskkill did not clean up the tunnel process tree")
	}
}
