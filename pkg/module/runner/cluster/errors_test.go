package cluster

import (
	"context"
	"errors"
	"fmt"
	"testing"

	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// definitionResource is the group/resource the classification cases
// write to.
var definitionResource = schema.GroupResource{
	Group:    "config.opendatahub.io",
	Resource: "platformmoduledefinitions",
}

// TestIsWriteOutcomeUnknown pins the write-outcome classification
// policy on constructed errors only: the ambiguity of cancellations,
// deadlines, timeout statuses and transport failures, and the
// definitive reading of every other Kubernetes status answer. It does
// not exercise client-go transport or retry behavior.
func TestIsWriteOutcomeUnknown(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		err  error
		want bool
	}{
		"nil error": {
			err:  nil,
			want: false,
		},
		"cancellation": {
			err:  context.Canceled,
			want: true,
		},
		"deadline": {
			err:  context.DeadlineExceeded,
			want: true,
		},
		"wrapped cancellation": {
			err:  fmt.Errorf("applying the definition owner: %w", context.Canceled),
			want: true,
		},
		"wrapped deadline": {
			err:  fmt.Errorf("applying the definition owner: %w", context.DeadlineExceeded),
			want: true,
		},
		"timeout status": {
			err:  apierrors.NewTimeoutError("the request timed out", 1),
			want: true,
		},
		"server timeout status": {
			err:  apierrors.NewServerTimeout(definitionResource, "patch", 1),
			want: true,
		},
		"wrapped server timeout status": {
			err:  fmt.Errorf("applying the definition owner: %w", apierrors.NewServerTimeout(definitionResource, "patch", 1)),
			want: true,
		},
		"transport failure": {
			err:  errors.New("connection refused"),
			want: true,
		},
		"forbidden status": {
			err:  apierrors.NewForbidden(definitionResource, "synthetic", errors.New("denied")),
			want: false,
		},
		"conflict status": {
			err:  apierrors.NewConflict(definitionResource, "synthetic", errors.New("conflict")),
			want: false,
		},
		"invalid status": {
			err: apierrors.NewInvalid(
				schema.GroupKind{Group: "config.opendatahub.io", Kind: "PlatformModuleDefinition"},
				"synthetic",
				field.ErrorList{field.Required(field.NewPath("spec"), "missing")},
			),
			want: false,
		},
		"wrapped forbidden status": {
			err:  fmt.Errorf("applying the definition owner: %w", apierrors.NewForbidden(definitionResource, "synthetic", errors.New("denied"))),
			want: false,
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)
			g.Expect(IsWriteOutcomeUnknown(c.err)).To(Equal(c.want))
		})
	}
}
