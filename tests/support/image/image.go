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

// Package image owns the controller image used by one end-to-end test suite.
package image

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/lburgazzoli/odh-platform-tools/tests/support/cluster"
)

// ExternalImageEnv selects the controller image for an explicitly configured cluster.
const ExternalImageEnv = "ODH_PLATFORM_TOOLS_E2E_IMAGE"

const (
	registry      = "quay.io/lburgazzoli"
	repository    = "odh-platform-tools"
	prepareBudget = 15 * time.Minute
	cleanupBudget = time.Minute
)

// Controller reports the suite reference and, for managed Kind, its local archive.
type Controller struct {
	reference string
	archive   string
	ownsTag   bool
	run       commandRunner
}

// Reference is the exact image name the suite deploys.
func (i *Controller) Reference() string { return i.reference }

// Archive is the managed import archive. External images have no local archive.
func (i *Controller) Archive() string { return i.archive }

// Prepare resolves an external image or builds and exports one native managed image.
func Prepare(ctx context.Context, engineType string, externalReference string, root string) (*Controller, error) {
	return prepare(ctx, engineType, externalReference, root, runCommand)
}

// ExternalReference requires an explicitly supplied image for an external cluster.
func ExternalReference(raw string) (string, error) {
	reference := strings.TrimSpace(raw)
	if reference == "" {
		return "", fmt.Errorf("the external engine requires %s with an image accessible to its nodes", ExternalImageEnv)
	}

	return reference, nil
}

func prepare(ctx context.Context, engineType string, externalReference string, root string, run commandRunner) (*Controller, error) {
	switch engineType {
	case cluster.TypeExternal:
		reference, err := ExternalReference(externalReference)
		if err != nil {
			return nil, err
		}

		return &Controller{reference: reference}, nil
	case cluster.TypeKind:
		ctx, cancel := context.WithTimeout(ctx, prepareBudget)
		defer cancel()

		image := &Controller{run: run}
		if err := image.buildAndExport(ctx, root); err != nil {
			return nil, errors.Join(err, image.Close(context.WithoutCancel(ctx)))
		}

		return image, nil
	default:
		return nil, fmt.Errorf("unsupported engine %q for controller image", engineType)
	}
}

func (i *Controller) buildAndExport(ctx context.Context, root string) error {
	tag := "e2e-" + strings.ToLower(rand.Text())
	i.reference = registry + "/" + repository + ":" + tag
	if present, err := i.exists(ctx); err != nil {
		return fmt.Errorf("checking controller image %s: %w", i.reference, err)
	} else if present {
		return fmt.Errorf("controller image %s already exists", i.reference)
	}

	// A failed build may still have assigned the tag, so claim it before make.
	i.ownsTag = true
	if _, err := i.run(ctx, "make", "-C", root, "IMAGE_REGISTRY="+registry, "IMAGE_NAME="+repository, "IMAGE_TAG="+tag, "container-build"); err != nil {
		return fmt.Errorf("building controller image %s: %w", i.reference, err)
	}

	platform, err := i.run(ctx, "podman", "image", "inspect", "--format", "{{.Os}}/{{.Architecture}}", i.reference)
	switch {
	case err != nil:
		return fmt.Errorf("inspecting controller image %s: %w", i.reference, err)
	case strings.TrimSpace(string(platform)) != "linux/"+runtime.GOARCH:
		return fmt.Errorf("controller image %s has platform %q, want linux/%s", i.reference, strings.TrimSpace(string(platform)), runtime.GOARCH)
	}

	dir, err := os.MkdirTemp("", "odh-platform-tools-e2e-image-")
	if err != nil {
		return fmt.Errorf("creating the controller image archive directory: %w", err)
	}

	i.archive = filepath.Join(dir, "controller.tar")
	if _, err := i.run(ctx, "podman", "save", "--format", "docker-archive", "--output", i.archive, i.reference); err != nil {
		return fmt.Errorf("exporting controller image %s: %w", i.reference, err)
	}

	return nil
}

// Close removes the managed run's exact tag and archive. It can be called
// after a partial build and is a no-op for an external image.
func (i *Controller) Close(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, cleanupBudget)
	defer cancel()

	var tagErr, archiveErr error
	if i.ownsTag {
		tagErr = i.removeTag(ctx)
		if tagErr == nil {
			i.ownsTag = false
		}
	}
	if i.archive != "" {
		archiveErr = errors.Join(remove(i.archive), remove(filepath.Dir(i.archive)))
		if archiveErr == nil {
			i.archive = ""
		}
	}

	return errors.Join(tagErr, archiveErr)
}

func (i *Controller) removeTag(ctx context.Context) error {
	present, err := i.exists(ctx)
	switch {
	case err != nil:
		return fmt.Errorf("checking whether to remove controller image tag %s: %w", i.reference, err)
	case !present:
		return nil
	}

	if _, err := i.run(ctx, "podman", "image", "untag", i.reference, i.reference); err != nil {
		return fmt.Errorf("removing controller image tag %s: %w", i.reference, err)
	}

	return nil
}

func (i *Controller) exists(ctx context.Context) (bool, error) {
	_, err := i.run(ctx, "podman", "image", "exists", i.reference)
	if err == nil {
		return true, nil
	}
	if status, ok := errors.AsType[interface {
		error
		ExitCode() int
	}](err); ok && status.ExitCode() == 1 {
		return false, nil
	}

	return false, err
}

func remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing controller image artifact %s: %w", path, err)
	}

	return nil
}
