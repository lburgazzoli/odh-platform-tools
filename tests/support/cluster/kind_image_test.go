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
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	"sigs.k8s.io/kind/pkg/cluster/nodes"
	kindexec "sigs.k8s.io/kind/pkg/exec"
)

const testImageArchive = "controller archive bytes"
const testImageReference = "quay.io/lburgazzoli/odh-platform-tools:e2e-owned"
const testCRIImage = `{"status":{"id":"sha256:imported"}}`

type imageNode struct {
	nodes.Node
	name      string
	imported  []byte
	inspected []string
	loadErr   error
	inspectFn func() error
}

func (n *imageNode) String() string { return n.name }

func (n *imageNode) Command(executable string, args ...string) kindexec.Cmd {
	return &imageCommand{run: func(stdin io.Reader, stdout io.Writer) error {
		switch executable {
		case "containerd":
			_, err := io.WriteString(stdout, "version = 3\n[plugins.'io.containerd.cri.v1.images']\nsnapshotter = 'overlayfs'\n")
			return err
		case "ctr":
			if n.loadErr != nil {
				return n.loadErr
			}
			var err error
			n.imported, err = io.ReadAll(stdin)
			return err
		case "crictl":
			n.inspected = append(n.inspected, args...)
			if n.inspectFn != nil {
				if err := n.inspectFn(); err != nil {
					return err
				}
			}
			_, err := io.WriteString(stdout, testCRIImage)
			return err
		default:
			return fmt.Errorf("unexpected kind node command %q", executable)
		}
	}}
}

type imageCommand struct {
	kindexec.Cmd
	run    func(io.Reader, io.Writer) error
	stdin  io.Reader
	stdout io.Writer
}

func (c *imageCommand) SetStdin(value io.Reader) kindexec.Cmd {
	c.stdin = value
	return c
}

func (c *imageCommand) SetStdout(value io.Writer) kindexec.Cmd {
	c.stdout = value
	return c
}

func (c *imageCommand) SetStderr(_ io.Writer) kindexec.Cmd { return c }
func (c *imageCommand) Run() error                         { return c.run(c.stdin, c.stdout) }

func writeImageArchive(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "controller.tar")
	NewWithT(t).Expect(os.WriteFile(path, []byte(testImageArchive), 0o600)).To(Succeed())

	return path
}

func TestKindImportsTheArchiveIntoItsOwnInternalNodes(t *testing.T) {
	g := NewWithT(t)
	control := &imageNode{name: "control"}
	worker := &imageNode{name: "worker"}
	provider := &fakeProvider{nodeList: []nodes.Node{control, worker}}
	engine := &Kind{opts: newOptions(), provider: provider, name: "owned-cluster"}

	g.Expect(engine.LoadImageArchive(t.Context(), writeImageArchive(t), testImageReference)).To(Succeed())
	g.Expect(provider.nodeRequests).To(Equal([]string{"owned-cluster"}))
	for _, node := range []*imageNode{control, worker} {
		g.Expect(string(node.imported)).To(Equal(testImageArchive))
		g.Expect(node.inspected).To(Equal([]string{"inspecti", testImageReference}))
	}
}

func TestKindImageLoadStopsOnImportFailure(t *testing.T) {
	g := NewWithT(t)
	failure := errors.New("archive refused")
	control := &imageNode{name: "control", loadErr: failure}
	worker := &imageNode{name: "worker"}
	provider := &fakeProvider{nodeList: []nodes.Node{control, worker}}
	engine := &Kind{opts: newOptions(), provider: provider, name: "owned-cluster"}

	err := engine.LoadImageArchive(t.Context(), writeImageArchive(t), testImageReference)

	g.Expect(err).To(MatchError(ContainSubstring(failure.Error())))
	g.Expect(control.inspected).To(BeEmpty())
	g.Expect(worker.imported).To(BeEmpty())
}

func TestKindImageLoadRequiresSuccessfulVerification(t *testing.T) {
	g := NewWithT(t)
	node := &imageNode{name: "control", inspectFn: func() error { return errors.New("image not in CRI") }}
	provider := &fakeProvider{nodeList: []nodes.Node{node}}
	engine := &Kind{opts: newOptions(), provider: provider, name: "owned-cluster"}

	err := engine.LoadImageArchive(t.Context(), writeImageArchive(t), testImageReference)

	g.Expect(err).To(MatchError(ContainSubstring("verifying imported image")))
	g.Expect(err).To(MatchError(ContainSubstring("image not in CRI")))
}

func TestKindImageLoadRespectsDeadlineAndDoesNotStartWhenCanceled(t *testing.T) {
	g := NewWithT(t)
	path := writeImageArchive(t)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	node := &imageNode{name: "control", inspectFn: func() error {
		<-ctx.Done()
		return ctx.Err()
	}}
	provider := &fakeProvider{nodeList: []nodes.Node{node}}
	engine := &Kind{opts: newOptions(), provider: provider, name: "owned-cluster"}

	g.Expect(engine.LoadImageArchive(ctx, path, testImageReference)).To(MatchError(context.DeadlineExceeded))
	g.Expect(engine.LoadImageArchive(ctx, path, testImageReference)).To(MatchError(context.DeadlineExceeded))
	g.Expect(provider.nodeRequests).To(Equal([]string{"owned-cluster"}))
}
