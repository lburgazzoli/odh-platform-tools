//go:build e2e

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

// Package e2e is the live end-to-end suite: the module deploy CLI and
// deployer chain against a real cluster managed by the test-only
// cluster engines (tests/support/cluster). The engine bootstraps
// lazily on first use and fails hard — never skips — when the live
// prerequisites are missing.
//
// Environment contract:
//
//	ODH_PLATFORM_TOOLS_E2E_ENGINE              kind (default) | external
//	ODH_PLATFORM_TOOLS_E2E_NODE_IMAGE          overrides the recorded node image pin
//	ODH_PLATFORM_TOOLS_E2E_EXTERNAL_KUBECONFIG required with the external engine
//	ODH_PLATFORM_TOOLS_E2E_EXTERNAL_CONTEXT    required with the external engine
//
// The suite compiles without setup or credentials through
// `make test-e2e-compile`; nothing here executes at compile time.
package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lburgazzoli/odh-platform-tools/tests/support/cluster"
)

// The engine teardown budget: bounded independently of any test
// context, so a stuck deletion cannot outlive the suite.
const stopBudget = cluster.DefaultDeleteBudget

var (
	engineOnce sync.Once
	liveEngine cluster.Engine
	engineErr  error
)

// TestMain builds the real CLI binary the suite executes, then stops
// the lazily created engine after the suite, bounded, and reports —
// never hides — a retained cluster. A failed teardown fails the suite
// even when the tests themselves passed.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "odh-platform-tools-e2e-cli")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: creating the CLI build directory: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	cliBinary = filepath.Join(dir, "odh-platform-tools")

	build := exec.Command("go", "build", "-o", cliBinary,
		"github.com/lburgazzoli/odh-platform-tools/cmd")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: building the CLI binary: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	if liveEngine != nil {
		ctx, cancel := context.WithTimeout(context.Background(), stopBudget)
		defer cancel()

		if code != 0 {
			// The suite failed: the cluster's diagnostics are
			// evidence, collected before anything is torn down.
			if collector, ok := liveEngine.(interface {
				CollectLogs(context.Context) error
			}); ok {
				if err := collector.CollectLogs(ctx); err != nil {
					fmt.Fprintf(os.Stderr, "e2e: collecting failure logs: %v\n", err)
				}
			}
		}

		if err := liveEngine.Stop(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "e2e: %v\n", err)

			if code == 0 {
				code = 1
			}
		}
	}

	os.Exit(code)
}

// engine returns the suite's live engine, creating and connecting it
// once on first use. It fails the test hard — a live target is never
// skipped — when the prerequisites are missing: no container runtime,
// an unsupported engine selection, or an unconfirmed external
// configuration. There is no current-context fallback anywhere.
func engine(t *testing.T) cluster.Engine {
	t.Helper()

	engineOnce.Do(func() {
		engineType, options, err := resolveEngine()
		if err != nil {
			engineErr = err
			return
		}

		created, err := cluster.New(t.Context(),
			append([]cluster.Option{cluster.WithType(engineType)}, options...)...)
		if err != nil {
			engineErr = fmt.Errorf("creating the %s engine: %w", engineType, err)
			return
		}

		if err := created.Setup(t.Context()); err != nil {
			engineErr = fmt.Errorf("connecting the %s engine: %w", engineType, err)

			// The engine owns the cluster: dispose it exactly, even
			// on the failure path.
			stopCtx, cancel := context.WithTimeout(context.Background(), stopBudget)
			defer cancel()

			if stopErr := created.Stop(stopCtx); stopErr != nil {
				fmt.Fprintf(os.Stderr, "e2e: %v\n", stopErr)
			}

			return
		}

		liveEngine = created
	})

	if engineErr != nil {
		t.Fatalf("e2e engine unavailable (this suite never skips live targets): %v", engineErr)
	}

	return liveEngine
}

// resolveEngine maps the environment onto the engine selection and
// its options, failing clearly — never falling back — for a missing
// engine runtime, an unsupported selection, or an unconfirmed
// external configuration.
func resolveEngine() (string, []cluster.Option, error) {
	engineType := os.Getenv("ODH_PLATFORM_TOOLS_E2E_ENGINE")
	if engineType == "" {
		engineType = cluster.TypeKind
	}

	options := []cluster.Option{cluster.WithLogFn(logf)}

	switch engineType {
	case cluster.TypeKind:
		if err := requireContainerRuntime(); err != nil {
			return "", nil, err
		}

		// The recorded node image pin applies unless explicitly
		// overridden; an explicitly empty override is a missing
		// image, not a fallback.
		nodeImage := os.Getenv("ODH_PLATFORM_TOOLS_E2E_NODE_IMAGE")
		if nodeImage != "" {
			options = append(options, cluster.WithNodeImage(nodeImage))
		}

		return engineType, options, nil

	case cluster.TypeExternal:
		kubeconfig := os.Getenv("ODH_PLATFORM_TOOLS_E2E_EXTERNAL_KUBECONFIG")
		contextName := os.Getenv("ODH_PLATFORM_TOOLS_E2E_EXTERNAL_CONTEXT")

		if kubeconfig == "" || contextName == "" {
			return "", nil, fmt.Errorf(
				"the external engine requires both ODH_PLATFORM_TOOLS_E2E_EXTERNAL_KUBECONFIG " +
					"and ODH_PLATFORM_TOOLS_E2E_EXTERNAL_CONTEXT (no current-context fallback)")
		}

		options = append(options,
			cluster.WithKubeconfig(kubeconfig),
			cluster.WithContext(contextName),
		)

		return engineType, options, nil

	default:
		return "", nil, fmt.Errorf(
			"unsupported ODH_PLATFORM_TOOLS_E2E_ENGINE value %q (kind or external)", engineType)
	}
}

// requireContainerRuntime proves a container runtime kind can drive
// is present, failing with the exact requirement otherwise.
func requireContainerRuntime() error {
	for _, runtime := range []string{"docker", "podman", "nerdctl"} {
		if _, err := exec.LookPath(runtime); err == nil {
			return nil
		}
	}

	return fmt.Errorf(
		"no container runtime found: the kind engine requires docker, podman, or nerdctl in PATH")
}

// logf is the engine progress logger: stderr lines prefixed for the
// test output, usable both from tests and the TestMain teardown.
func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "e2e engine: "+format+"\n", args...)
}

// TestEnginePrerequisites proves the live environment resolves
// without creating anything: the engine selection, the container
// runtime for kind, and the external confirmation. It fails hard on
// a missing prerequisite — a live target is never skipped — and
// never touches a cluster.
func TestEnginePrerequisites(t *testing.T) {
	engineType, _, err := resolveEngine()
	if err != nil {
		t.Fatalf("e2e engine unavailable (this suite never skips live targets): %v", err)
	}

	if engineType == cluster.TypeKind {
		// The resolved node image is the recorded pin or its
		// explicit override — never empty.
		resolved := os.Getenv("ODH_PLATFORM_TOOLS_E2E_NODE_IMAGE")
		if resolved == "" {
			resolved = cluster.DefaultNodeImage
		}

		if resolved == "" {
			t.Fatal("e2e engine unavailable: no node image resolved")
		}

		t.Logf("engine: kind, node image: %s", resolved)
	}
}
