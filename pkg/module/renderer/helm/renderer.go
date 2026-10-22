// Package helm renders Helm-based module definitions through the
// actual framework Helm action over the configured local chart.
package helm

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	fwhelm "github.com/opendatahub-io/odh-platform-utilities/framework/controller/actions/render/helm"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/renderer"
)

// Render renders the module described by document through the actual
// framework Helm action and returns the rendered objects.
//
// The action renders the configured chart directory directly, with
// caching disabled and no client or release state attached. Chart
// strictness, values schema validation, bundled CRDs, dependency
// handling, content metadata, and output ordering all come from the
// framework action: this wrapper only maps the chart path and the
// module name as the Helm release name. It never resolves replacement
// strings, never sets values, and never adds repository configuration,
// so descriptor images and environment entries cannot affect the
// output.
//
// The returned list contains every object the framework action
// rendered for the chart; callers filtering it must not assume they
// hold the raw chart documents.
func Render(ctx context.Context, document config.Document) ([]unstructured.Unstructured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	request := renderer.NewRequest(document)
	action := fwhelm.NewAction(fwhelm.WithCache(false))

	if err := action(ctx, request); err != nil {
		return nil, fmt.Errorf("helm render for module %s: %w", document.Definition.Metadata.Name, err)
	}

	return request.Resources, nil
}
