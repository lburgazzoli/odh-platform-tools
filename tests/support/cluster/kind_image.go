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

	"sigs.k8s.io/kind/pkg/cluster/nodes"
	"sigs.k8s.io/kind/pkg/cluster/nodeutils"
)

// LoadImageArchive imports the exact reference into each owned internal node.
// The suite calls this between Setup and workload creation.
func (e *Kind) LoadImageArchive(ctx context.Context, archive string, reference string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, e.opts.CreateBudget)
	defer cancel()

	return supervise(ctx, "importing controller image into kind cluster "+e.name,
		e.opts.CreateBudget, e.opts.SuperviseGrace, e.opts.LogFn, func() error {
			nodes, err := e.provider.ListInternalNodes(e.name)
			switch {
			case err != nil:
				return fmt.Errorf("listing internal nodes of kind cluster %q: %w", e.name, err)
			case len(nodes) == 0:
				return fmt.Errorf("kind cluster %q has no internal nodes for image import", e.name)
			}

			for _, node := range nodes {
				if err := importImage(ctx, node, archive, reference); err != nil {
					return fmt.Errorf("loading %s into kind node %s: %w", reference, node, err)
				}
			}

			return nil
		})
}

func importImage(ctx context.Context, node nodes.Node, archive string, reference string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	stream, err := os.Open(archive)
	if err != nil {
		return fmt.Errorf("opening image archive: %w", err)
	}

	err = nodeutils.LoadImageArchive(node, stream)
	if err := errors.Join(err, stream.Close(), ctx.Err()); err != nil {
		return err
	}

	id, err := nodeutils.ImageID(node, reference)
	switch {
	case err != nil:
		return fmt.Errorf("verifying imported image: %w", err)
	case id == "":
		return fmt.Errorf("imported image has no ID")
	default:
		return ctx.Err()
	}
}
