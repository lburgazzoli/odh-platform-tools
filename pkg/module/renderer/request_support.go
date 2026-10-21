package renderer

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/opendatahub-io/odh-platform-utilities/framework/api"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
)

// ModuleInstance is an in-memory framework PlatformObject carrying only
// the authored identity of a module: its custom resource type and name.
// It exists because the framework render actions read instance metadata
// (the kind, for metrics) even when caching is disabled.
//
// It deliberately carries no cluster identity: no UID, generation,
// resource version, namespace, or owner references. Actions that need
// those are cluster actions and are out of scope for offline rendering.
type ModuleInstance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`

	status api.Status
}

// ModuleInstance satisfies the framework instance interface.
var _ api.PlatformObject = (*ModuleInstance)(nil)

// NewInstance returns the in-memory module instance for a definition,
// with the custom resource type from spec.type and the module name.
func NewInstance(definition config.PlatformModuleDefinition) *ModuleInstance {
	return &ModuleInstance{
		TypeMeta: metav1.TypeMeta{
			APIVersion: definition.Spec.Type.Group + "/" + definition.Spec.Type.Version,
			Kind:       definition.Spec.Type.Kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: definition.Metadata.Name,
		},
	}
}

// GetStatus returns the in-memory status block.
func (i *ModuleInstance) GetStatus() *api.Status {
	return &i.status
}

// GetConditions returns the status conditions.
func (i *ModuleInstance) GetConditions() []api.Condition {
	return i.status.Conditions
}

// SetConditions replaces the status conditions with a copy, so later
// mutation of the argument does not change the instance.
func (i *ModuleInstance) SetConditions(conditions []api.Condition) {
	i.status.SetConditions(conditions)
}

// DeepCopy returns a deep copy of the instance. Mutable metadata
// (labels, annotations, owner references, conditions) is copied through
// the apimachinery and framework deep-copy support and is never aliased
// with the receiver.
func (i *ModuleInstance) DeepCopy() *ModuleInstance {
	if i == nil {
		return nil
	}

	out := new(ModuleInstance)
	*out = *i
	out.ObjectMeta = *i.ObjectMeta.DeepCopy()
	out.status = *i.status.DeepCopy()

	return out
}

// DeepCopyObject implements runtime.Object.
func (i *ModuleInstance) DeepCopyObject() runtime.Object {
	return i.DeepCopy()
}
