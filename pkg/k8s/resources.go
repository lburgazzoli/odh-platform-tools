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

// DecodeResources decodes a YAML stream — one or more documents
// separated by --- — into unstructured resources, preserving document
// order. Empty documents and documents without a kind are skipped;
// anything undecodable fails. It delegates to the framework's shared
// document decoder, the same resource decoding the render actions use.
func DecodeResources(content []byte) ([]unstructured.Unstructured, error) {
	// An empty scheme suffices: decoding into unstructured never
	// consults the type registry, so any group/version/kind —
	// including unknown ones — decodes exactly as it would with a
	// populated scheme.
	decoder := serializer.NewCodecFactory(runtime.NewScheme()).UniversalDeserializer()

	return fwresources.Decode(decoder, content)
}

// Is reports whether candidate and target identify the same object:
// equal group/version/kind, namespace, and name. An object carrying no
// group/version/kind never matches, so a freshly built typed object
// must stamp one first.
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
