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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	. "github.com/onsi/gomega"
)

// These tests spawn real helper subprocesses to prove the
// supervision contract, so they never run in parallel with each
// other: a supervised operation must be able to attribute every
// process it spawned — new since its baseline and descending from
// this binary — to itself, and only those.

// TestSuperviseReturnsOperationResult proves an operation that
// finishes inside its budget returns its own result untouched.
func TestSuperviseReturnsOperationResult(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	sentinel := errors.New("operation failed")

	err := supervise(t.Context(), "operation", time.Minute, time.Minute, nil,
		func() error { return sentinel })
	g.Expect(err).To(MatchError(sentinel))

	err = supervise(t.Context(), "operation", time.Minute, time.Minute, nil,
		func() error { return nil })
	g.Expect(err).NotTo(HaveOccurred())
}

// TestSuperviseBoundsStuckOperation proves a stuck operation is
// bounded: its budget expires, the helper subprocess it spawned is
// killed, and the supervised goroutine returns before supervise
// does — nothing is left running.
func TestSuperviseBoundsStuckOperation(t *testing.T) {
	g := NewWithT(t)

	returned := make(chan error, 1)

	err := supervise(t.Context(), "stuck operation", 150*time.Millisecond, 10*time.Second, nil,
		func() error {
			// The helper spawns after the supervision baseline, like
			// the kind provider's own runtime helpers.
			command := exec.Command("sleep", "300")

			if startErr := command.Start(); startErr != nil {
				return startErr
			}

			// Blocks until the supervisor kills the helper.
			waitErr := command.Wait()

			returned <- waitErr

			return errors.New("operation was terminated")
		})
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("stuck operation exceeded"))
	g.Expect(err.Error()).To(ContainSubstring("150ms"))

	// The goroutine joined: its result arrived, and the helper was
	// killed rather than still sleeping.
	select {
	case waitErr := <-returned:
		g.Expect(waitErr).To(HaveOccurred())
	case <-time.After(10 * time.Second):
		t.Fatal("the supervised goroutine did not return after its helper was killed")
	}
}

// TestSuperviseKillsProcessTree proves termination covers the whole
// helper tree: the direct child and its own child both die.
func TestSuperviseKillsProcessTree(t *testing.T) {
	g := NewWithT(t)

	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")

	shExited := make(chan error, 1)

	err := supervise(t.Context(), "tree operation", 150*time.Millisecond, 10*time.Second, nil,
		func() error {
			// The direct child is a shell whose own child — the
			// grandchild — records its PID before sleeping.
			script := "sleep 300 & echo $! > " + pidFile + "; wait"

			command := exec.Command("sh", "-c", script)

			if startErr := command.Start(); startErr != nil {
				return startErr
			}

			shExited <- command.Wait()

			return errors.New("operation was terminated")
		})
	g.Expect(err).To(HaveOccurred())

	select {
	case waitErr := <-shExited:
		g.Expect(waitErr).To(HaveOccurred())
	case <-time.After(10 * time.Second):
		t.Fatal("the direct helper child did not return after the kill")
	}

	// The grandchild died with its parent tree.
	g.Eventually(func() bool {
		content, readErr := os.ReadFile(pidFile)
		if readErr != nil {
			return false
		}

		pid, parseErr := strconv.Atoi(string(content[:len(content)-1]))
		if parseErr != nil {
			return false
		}

		// Signal 0 probes existence: ESRCH means the process is gone.
		return syscall.Kill(pid, 0) != nil
	}).WithTimeout(10 * time.Second).WithPolling(50 * time.Millisecond).Should(BeTrue())
}

// TestSuperviseSparesUnrelatedProcesses proves termination never
// reaches processes this test binary did not spawn: an orphan
// re-parented away from the binary — standing in for any unrelated
// host process that appears during the supervised window — survives
// a supervised operation's timeout kill, while the operation's own
// helper is killed and the goroutine joins.
func TestSuperviseSparesUnrelatedProcesses(t *testing.T) {
	g := NewWithT(t)

	dir := t.TempDir()
	pidFile := filepath.Join(dir, "orphan.pid")

	err := supervise(t.Context(), "orphan-sparing operation", 150*time.Millisecond, 10*time.Second, nil,
		func() error {
			// A double-forked sleep: the shell exits at once and the
			// background sleep re-parents away from this binary — a
			// new process whose ancestry no longer reaches it, like a
			// sibling build or desktop helper spawned during the
			// supervised window.
			script := "sleep 300 & echo $! > " + pidFile + "; exit"

			detached := exec.Command("sh", "-c", script)

			if startErr := detached.Start(); startErr != nil {
				return startErr
			}

			shPID := detached.Process.Pid

			if waitErr := detached.Wait(); waitErr != nil {
				return waitErr
			}

			// The orphan must be re-parented before the budget
			// expires, or the kill would legitimately see it as our
			// own still-attached child.
			deadline := time.Now().Add(10 * time.Second)

			for {
				content, readErr := os.ReadFile(pidFile)
				if readErr == nil {
					if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(content))); parseErr == nil {
						if ppid, ok := parentPID(pid); ok && ppid != shPID {
							break
						}
					}
				}

				if time.Now().After(deadline) {
					return errors.New("the orphan never re-parented away from the shell")
				}

				time.Sleep(10 * time.Millisecond)
			}

			// The operation's own helper: the kill takes it, which
			// unblocks this goroutine inside the grace period.
			stuck := exec.Command("sleep", "300")

			if startErr := stuck.Start(); startErr != nil {
				return startErr
			}

			waitErr := stuck.Wait()

			return fmt.Errorf("operation was terminated: %v", waitErr)
		})
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("orphan-sparing operation exceeded"))

	content, readErr := os.ReadFile(pidFile)
	g.Expect(readErr).NotTo(HaveOccurred())

	orphanPID, parseErr := strconv.Atoi(strings.TrimSpace(string(content)))
	g.Expect(parseErr).NotTo(HaveOccurred())

	// The unrelated process survived the kill: still alive, still
	// re-parented away.
	g.Eventually(func() bool {
		ppid, ok := parentPID(orphanPID)
		return ok && ppid != os.Getpid()
	}).WithTimeout(5 * time.Second).WithPolling(50 * time.Millisecond).Should(BeTrue())

	// The test cleans up the process it spawned on its own behalf.
	g.Expect(syscall.Kill(orphanPID, syscall.SIGKILL)).To(Succeed())
}

// TestSuperviseParentDeadlineWins proves the caller's deadline caps
// the operation budget.
func TestSuperviseParentDeadlineWins(t *testing.T) {
	g := NewWithT(t)

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	started := time.Now()

	err := supervise(ctx, "parent-bounded operation", time.Hour, 2*time.Second, nil,
		func() error {
			time.Sleep(time.Minute)
			return nil
		})
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("parent-bounded operation exceeded"))
	g.Expect(time.Since(started)).To(BeNumerically("<", 5*time.Second))
}
