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

package k8s

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"

	fwresources "github.com/opendatahub-io/odh-platform-utilities/framework/resources"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DecodeResources decodes YAML documents in order, skipping empty or kind-less
// documents. Unknown kinds are supported.
func DecodeResources(content []byte) ([]unstructured.Unstructured, error) {
	// Unstructured decoding does not require registered types.
	decoder := serializer.NewCodecFactory(runtime.NewScheme()).UniversalDeserializer()

	return fwresources.Decode(decoder, content)
}

// Is compares GVK, namespace, and name. An empty GVK never matches.
func Is(candidate client.Object, target client.Object) bool {
	candidateGVK := candidate.GetObjectKind().GroupVersionKind()
	targetGVK := target.GetObjectKind().GroupVersionKind()

	switch {
	case candidateGVK.Empty():
		return false
	case targetGVK.Empty():
		return false
	case candidateGVK != targetGVK:
		return false
	case candidate.GetNamespace() != target.GetNamespace():
		return false
	default:
		return candidate.GetName() == target.GetName()
	}
}
