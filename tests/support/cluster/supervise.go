/*
Copyright 2026 Luca Burgazzoli

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cluster

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// supervise runs one provider helper operation under a parent
// deadline.
//
// kind v0.32's provider methods — Create, Delete, KubeConfig,
// CollectLogs — take no context: every one of them blocks on helper
// subprocesses (container runtime, kubectl) it spawns as direct
// children of this process. supervise runs the operation on a
// supervised goroutine and bounds it: when the operation's budget
// expires, every helper process tree that appeared after the
// operation started — and only those, identified by parentage, never
// processes the test binary already owned — is killed, which is what
// unblocks the provider call, and the goroutine is then joined
// within a bounded grace period. On the happy path and after every
// successful termination the goroutine has returned before supervise
// does; only an operation that survives the kill of its whole helper
// tree and the grace period is reported as still running, and that
// error names it.
//
// The budget derives from the caller's context through
// context.WithTimeout, so the parent deadline always wins.
func supervise(
	ctx context.Context,
	operation string,
	budget time.Duration,
	grace time.Duration,
	logFn func(format string, args ...any),
	fn func() error,
) error {
	if fn == nil {
		return nil
	}

	if budget <= 0 {
		budget = DefaultCreateBudget
	}

	if grace <= 0 {
		grace = DefaultSuperviseGrace
	}

	// A supervisor without a logger stays silent; the report is the
	// error, not the log line.
	if logFn == nil {
		logFn = func(string, ...any) {}
	}

	runCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	baseline := childProcessTrees()

	done := make(chan error, 1)
	go func() {
		done <- fn()
	}()

	select {
	case err := <-done:
		return err
	case <-runCtx.Done():
		killed := killNewProcessTrees(baseline)

		logFn("%s exceeded its %s budget and was terminated (%d helper processes killed)",
			operation, budget, killed)

		joinTimer := time.NewTimer(grace)
		defer joinTimer.Stop()

		select {
		case opErr := <-done:
			return fmt.Errorf("%s exceeded its %s budget and was terminated (operation result: %v): %w",
				operation, budget, opErr, runCtx.Err())
		case <-joinTimer.C:
			return fmt.Errorf("%s exceeded its %s budget, its helper processes were killed, "+
				"and it still had not returned within %s: it may still be running",
				operation, budget, grace)
		}
	}
}

// childProcessTrees snapshots every process of this test binary's
// process tree keyed by PID, with each process's parent, so a later
// snapshot can identify exactly the trees an operation spawned. A
// non-Linux environment without /proc yields an empty snapshot: the
// bounded join still applies, only the kill does not.
func childProcessTrees() map[int]int {
	trees := map[int]int{}

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return trees
	}

	self := os.Getpid()

	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == self {
			continue
		}

		ppid, ok := parentPID(pid)
		if !ok {
			continue
		}

		trees[pid] = ppid
	}

	return trees
}

// killNewProcessTrees kills every process tree rooted at a process
// that appeared after the baseline snapshot — the helper trees a
// supervised operation spawned — and returns how many processes were
// signalled. Processes alive at the baseline, and their existing
// descendants, are never touched.
func killNewProcessTrees(baseline map[int]int) int {
	current := childProcessTrees()

	newRoots := map[int]struct{}{}
	for pid := range current {
		if _, old := baseline[pid]; !old {
			newRoots[pid] = struct{}{}
		}
	}

	// A new root's descendants are new too, wherever they attach;
	// resolving transitively covers helper trees that re-parent.
	doomed := map[int]struct{}{}
	for root := range newRoots {
		doomed[root] = struct{}{}
	}

	for changed := true; changed; {
		changed = false

		for pid, ppid := range current {
			if _, selected := doomed[pid]; selected {
				continue
			}

			if _, selected := doomed[ppid]; selected {
				doomed[pid] = struct{}{}
				changed = true
			}
		}
	}

	killed := 0
	for pid := range doomed {
		if err := syscall.Kill(pid, syscall.SIGKILL); err == nil {
			killed++
		}
	}

	return killed
}

// parentPID reads one process's parent PID from /proc. The comm
// field may contain spaces and parentheses, so the status fields are
// parsed after its closing parenthesis.
func parentPID(pid int) (int, bool) {
	status, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, false
	}

	closing := strings.LastIndexByte(string(status), ')')
	if closing < 0 || closing+2 >= len(status) {
		return 0, false
	}

	// Fields after the comm: state, ppid, ...
	fields := strings.Fields(string(status[closing+2:]))
	if len(fields) < 2 {
		return 0, false
	}

	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}

	return ppid, true
}
