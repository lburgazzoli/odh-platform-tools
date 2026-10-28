package deployer

import (
	"errors"
	"fmt"
	"testing"

	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

func TestSafeFailureMessage(t *testing.T) {
	t.Parallel()
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "settings", errors.New("SECRET-BODY"))
	cases := []struct {
		name  string
		cause error
		want  string
	}{
		{name: "wrapped status", cause: fmt.Errorf("SECRET-WRAPPER: %w", forbidden), want: "apply failed: Forbidden (configmaps/settings)"},
		{name: "discovery", cause: &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "example.io", Kind: "Widget"}}, want: "apply failed: NoMatch (example.io/Widget)"},
		{name: "unknown error", cause: errors.New("SECRET-BODY"), want: "apply failed"},
		{name: "unknown reason", cause: &apierrors.StatusError{ErrStatus: metav1.Status{Reason: "SECRET-REASON"}}, want: "apply failed"},
		{name: "invalid identity", cause: apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "invalid\nidentity"), want: "apply failed: NotFound (pods)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := NewWithT(t)
			message := safeFailureMessage("apply failed", tc.cause)
			g.Expect(message).To(Equal(tc.want))
			err := moduleerrors.New(moduleerrors.ErrDeployApply, message, moduleerrors.WithCause(tc.cause))
			g.Expect(errors.Is(err, tc.cause)).To(BeTrue())
		})
	}
}
