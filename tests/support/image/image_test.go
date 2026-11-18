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

package image

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/lburgazzoli/odh-platform-tools/tests/support/cluster"
)

type exitCode int

func (c exitCode) Error() string { return "command exit" }
func (c exitCode) ExitCode() int { return int(c) }

func TestExternalImagePreflightNeverInvokesLocalImageCommands(t *testing.T) {
	g := NewWithT(t)
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		t.Fatalf("unexpected local command %s %v for external engine", name, args)
		return nil, nil
	}
	external := "registry.example.com/tools/mock:v1"

	missing, err := prepare(t.Context(), cluster.TypeExternal, " ", "", run)
	g.Expect(missing).To(BeNil())
	g.Expect(err).To(MatchError(ContainSubstring(ExternalImageEnv)))

	resolved, err := prepare(t.Context(), cluster.TypeExternal, external, "", run)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(resolved.Reference()).To(Equal(external))
	g.Expect(resolved.Archive()).To(BeEmpty())
	g.Expect(resolved.Close(t.Context())).To(Succeed())
}

func TestManagedImageBuildsAndCleansOneExactTagAndArchive(t *testing.T) {
	g := NewWithT(t)
	script := imageCommands{}
	root := t.TempDir()

	result, err := prepare(t.Context(), cluster.TypeKind, "", root, script.run)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.Reference()).To(HavePrefix("quay.io/lburgazzoli/odh-platform-tools:e2e-"))
	g.Expect(result.Reference()).To(Equal(script.reference))
	g.Expect(result.Archive()).To(BeARegularFile())
	g.Expect(script.calls).To(Equal([][]string{
		{"podman", "image", "exists", script.reference},
		{"make", "-C", root, "IMAGE_REGISTRY=quay.io/lburgazzoli", "IMAGE_NAME=odh-platform-tools", "IMAGE_TAG=" + strings.TrimPrefix(script.reference, registry+"/"+repository+":"), "image"},
		{"podman", "image", "inspect", "--format", "{{.Os}}/{{.Architecture}}", script.reference},
		{"podman", "save", "--format", "docker-archive", "--output", script.archive, script.reference},
	}))

	g.Expect(result.Close(t.Context())).To(Succeed())
	g.Expect(result.Close(t.Context())).To(Succeed())
	g.Expect(script.calls[4:]).To(Equal([][]string{
		{"podman", "image", "exists", script.reference},
		{"podman", "image", "untag", script.reference, script.reference},
	}))
	g.Expect(filepath.Dir(script.archive)).NotTo(BeAnExistingFile())
}

func TestManagedImageLeavesAnExistingReferenceAlone(t *testing.T) {
	g := NewWithT(t)
	script := imageCommands{collision: true}
	result, err := prepare(t.Context(), cluster.TypeKind, "", t.TempDir(), script.run)

	g.Expect(result).To(BeNil())
	g.Expect(err).To(MatchError(ContainSubstring("already exists")))
	g.Expect(script.calls).To(Equal([][]string{{"podman", "image", "exists", script.reference}}))
}

func TestManagedImageCleansPartialSetupFailures(t *testing.T) {
	for _, stage := range []string{"make", "inspect", "save"} {
		t.Run(stage, func(t *testing.T) {
			g := NewWithT(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			script := imageCommands{fail: stage, cancel: cancel}

			result, err := prepare(ctx, cluster.TypeKind, "", t.TempDir(), script.run)

			g.Expect(result).To(BeNil())
			g.Expect(err).To(MatchError(errImageStage))
			g.Expect(script.calls[len(script.calls)-2:]).To(Equal([][]string{
				{"podman", "image", "exists", script.reference},
				{"podman", "image", "untag", script.reference, script.reference},
			}))
			if script.archive != "" {
				g.Expect(filepath.Dir(script.archive)).NotTo(BeAnExistingFile())
			}
		})
	}
}

func TestManagedImageReportsCleanupFailureWithoutKeepingTheArchive(t *testing.T) {
	g := NewWithT(t)
	script := imageCommands{fail: "save", untagErr: errors.New("tag cleanup denied")}

	result, err := prepare(t.Context(), cluster.TypeKind, "", t.TempDir(), script.run)

	g.Expect(result).To(BeNil())
	g.Expect(err).To(MatchError(errImageStage))
	g.Expect(err).To(MatchError(script.untagErr))
	g.Expect(filepath.Dir(script.archive)).NotTo(BeAnExistingFile())
}

var errImageStage = errors.New("selected image stage failed")

type imageCommands struct {
	calls     [][]string
	reference string
	archive   string
	fail      string
	collision bool
	cancel    context.CancelFunc
	untagErr  error
}

func (s *imageCommands) run(ctx context.Context, executable string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.calls = append(s.calls, append([]string{executable}, args...))
	stage := args[0]
	if stage == "image" {
		stage = args[1]
	}

	switch stage {
	case "exists":
		if s.reference == "" {
			s.reference = args[2]
			if !s.collision {
				return nil, exitCode(1)
			}
		}
	case "save":
		if len(args) != 6 || args[3] != "--output" || !filepath.IsAbs(args[4]) {
			return nil, fmt.Errorf("unexpected image save arguments: %v", args)
		}
		s.archive = args[4]
		// Simulate Podman leaving a partial output before returning an error.
		if err := os.WriteFile(s.archive, []byte("partial archive"), 0o600); err != nil {
			return nil, err
		}
	case "untag":
		return nil, s.untagErr
	}

	if executable == "make" {
		stage = "make"
	}
	if stage == s.fail {
		if s.cancel != nil {
			s.cancel()
		}
		return nil, errImageStage
	}
	if stage == "inspect" {
		return []byte("linux/" + runtime.GOARCH + "\n"), nil
	}

	return nil, nil
}
