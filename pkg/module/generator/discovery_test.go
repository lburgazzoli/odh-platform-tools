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

package generator

import (
	"errors"
	"testing"

	. "github.com/onsi/gomega"

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// TestDiscoverModuleInfersGVKFromStorageVersion proves discovery reads
// the module GVK from the CRD's unique served storage version.
func TestDiscoverModuleInfersGVKFromStorageVersion(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	discovery, err := discoverModule(validModuleResources(t))

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(discovery.moduleType).To(Equal(fooBarType()))
}

// TestDiscoverModuleRejectsBadCardinality proves zero or multiple
// CRDs and Deployments fail regardless of any other content.
func TestDiscoverModuleRejectsBadCardinality(t *testing.T) {
	t.Parallel()

	crd := func(t *testing.T) []any {
		return []any{moduleCRD(t, "example.com", "FooBar",
			crdVersionSpec{name: "v1alpha1", served: true, storage: true})}
	}
	deployment := func(t *testing.T) []any {
		return []any{controllerDeployment(t, "foo-controller",
			[]containerSpec{{name: "manager", image: "img"}}, nil)}
	}

	for _, tc := range []struct {
		name      string
		resources []any
	}{
		{name: "empty bundle", resources: nil},
		{name: "crd only", resources: crd(t)},
		{name: "deployment only", resources: deployment(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			_, err := discoverModule(toUnstructureds(t, tc.resources...))

			g.Expect(err).To(HaveOccurred())
			g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())
		})
	}

	t.Run("two crds", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		_, err := discoverModule(toUnstructureds(t,
			moduleCRD(t, "example.com", "FooBar", crdVersionSpec{name: "v1alpha1", served: true, storage: true}),
			moduleCRD(t, "other.com", "Other", crdVersionSpec{name: "v1alpha1", served: true, storage: true}),
			controllerDeployment(t, "foo-controller", []containerSpec{{name: "manager", image: "img"}}, nil),
		))

		g.Expect(err).To(HaveOccurred())
	})

	t.Run("two deployments", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		_, err := discoverModule(toUnstructureds(t,
			moduleCRD(t, "example.com", "FooBar", crdVersionSpec{name: "v1alpha1", served: true, storage: true}),
			controllerDeployment(t, "one", []containerSpec{{name: "manager", image: "img"}}, nil),
			controllerDeployment(t, "two", []containerSpec{{name: "manager", image: "img"}}, nil),
		))

		g.Expect(err).To(HaveOccurred())
	})
}

// TestDiscoverModuleRejectsWrongGVKs proves unsupported CRD and
// Deployment apiVersions fail.
func TestDiscoverModuleRejectsWrongGVKs(t *testing.T) {
	t.Parallel()

	t.Run("beta crd", func(t *testing.T) {
		t.Parallel()

		g := NewWithT(t)

		_, err := discoverModule(toUnstructureds(t,
			betaCRD(t),
			controllerDeployment(t, "foo-controller", []containerSpec{{name: "manager", image: "img"}}, nil),
		))

		g.Expect(err).To(HaveOccurred())
		g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())
	})
}

// TestDiscoverModuleRejectsMalformedCRDs proves unusable groups,
// kinds, and version tables fail as definition errors.
func TestDiscoverModuleRejectsMalformedCRDs(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		group    string
		kind     string
		versions []crdVersionSpec
	}{
		{
			name:  "unusable group",
			group: "Example.COM",
			kind:  "FooBar",
			versions: []crdVersionSpec{
				{name: "v1alpha1", served: true, storage: true},
			},
		},
		{
			name:  "lowercase kind",
			group: "example.com",
			kind:  "foobar",
			versions: []crdVersionSpec{
				{name: "v1alpha1", served: true, storage: true},
			},
		},
		{
			name:  "no storage version",
			group: "example.com",
			kind:  "FooBar",
			versions: []crdVersionSpec{
				{name: "v1alpha1", served: true, storage: false},
			},
		},
		{
			name:  "multiple storage versions",
			group: "example.com",
			kind:  "FooBar",
			versions: []crdVersionSpec{
				{name: "v1alpha1", served: true, storage: true},
				{name: "v1beta1", served: true, storage: true},
			},
		},
		{
			name:  "unserved storage version",
			group: "example.com",
			kind:  "FooBar",
			versions: []crdVersionSpec{
				{name: "v1alpha1", served: false, storage: true},
				{name: "v1beta1", served: true, storage: false},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			_, err := discoverModule(toUnstructureds(t,
				moduleCRD(t, tc.group, tc.kind, tc.versions...),
				controllerDeployment(t, "foo-controller",
					[]containerSpec{{name: "manager", image: "img"}}, nil),
			))

			g.Expect(err).To(HaveOccurred())
			g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())
		})
	}
}

// TestConfirmModuleRejectsIdentityDrift proves the final render must
// carry the CRD identity discovered at bootstrap.
func TestConfirmModuleRejectsIdentityDrift(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	discovery := moduleDiscovery{moduleType: fooBarType()}

	_, err := confirmModule(toUnstructureds(t,
		moduleCRD(t, "example.com", "Other",
			crdVersionSpec{name: "v1alpha1", served: true, storage: true}),
		controllerDeployment(t, "foo-controller", []containerSpec{{name: "manager", image: "img"}}, nil),
	), discovery)

	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("changed"))
}

// TestConfirmModuleDecodesDeployment proves a matching final render
// yields the decoded controller Deployment.
func TestConfirmModuleDecodesDeployment(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	discovery := moduleDiscovery{moduleType: fooBarType()}

	deployment, err := confirmModule(validModuleResources(t), discovery)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(deployment.Name).To(Equal("foo-controller"))
	g.Expect(deployment.Spec.Template.Spec.Containers).To(HaveLen(2))
}

// TestDiscoverControllerCoversMainSelection proves the main container
// rules: unique manager among several, sole container, and ambiguity
// without a unique manager.
func TestDiscoverControllerCoversMainSelection(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		containers []containerSpec
		expectName string
		expectErr  bool
	}{
		{
			name: "manager among sidecars",
			containers: []containerSpec{
				{name: "manager", image: "quay.io/example/controller:v1"},
				{name: "sidecar", image: "quay.io/example/sidecar:v1"},
			},
			expectName: "manager",
		},
		{
			name:       "sole non-manager container",
			containers: []containerSpec{{name: "controller", image: "quay.io/example/controller:v1"}},
			expectName: "controller",
		},
		{
			name:       "no containers",
			containers: nil,
			expectErr:  true,
		},
		{
			name: "no unique manager",
			containers: []containerSpec{
				{name: "one", image: "a"},
				{name: "two", image: "b"},
			},
			expectErr: true,
		},
		{
			name: "duplicate names",
			containers: []containerSpec{
				{name: "same", image: "a"},
				{name: "same", image: "b"},
			},
			expectErr: true,
		},
		{
			name:       "unnamed container",
			containers: []containerSpec{{name: "", image: "a"}},
			expectErr:  true,
		},
		{
			name:       "empty selected image",
			containers: []containerSpec{{name: "manager", image: ""}},
			expectErr:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			deployment := typedDeployment(t, controllerDeployment(t,
				"foo-controller", tc.containers, nil))

			selection, err := discoverController(deployment, "")

			if tc.expectErr {
				g.Expect(err).To(HaveOccurred())
				g.Expect(errors.Is(err, moduleerrors.ErrInvalidDefinition)).To(BeTrue())

				return
			}

			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(selection.containerName).To(Equal(tc.expectName))
			g.Expect(selection.deploymentName).To(Equal("foo-controller"))
		})
	}
}

// TestDiscoverControllerCoversInitSelection proves the init container
// rules: mandatory explicit selection, matching, mismatch, and empty
// image.
func TestDiscoverControllerCoversInitSelection(t *testing.T) {
	t.Parallel()

	manager := []containerSpec{{name: "manager", image: "quay.io/example/controller:v1"}}

	for _, tc := range []struct {
		name        string
		containers  []containerSpec
		inits       []containerSpec
		selection   string
		expectErr   bool
		expectCat   error
		expectInit  string
		expectImage string
	}{
		{
			name:       "no init containers and none requested",
			containers: manager,
		},
		{
			name:       "init requested without init containers",
			containers: manager,
			selection:  "setup",
			expectErr:  true,
			expectCat:  moduleerrors.ErrInvalidGenerateOptions,
		},
		{
			name:       "init containers without selection",
			containers: manager,
			inits:      []containerSpec{{name: "setup", image: "quay.io/example/controller:v1"}},
			expectErr:  true,
			expectCat:  moduleerrors.ErrInvalidGenerateOptions,
		},
		{
			name:        "sole matching init",
			containers:  manager,
			inits:       []containerSpec{{name: "setup", image: "quay.io/example/controller:v1"}},
			selection:   "setup",
			expectInit:  "setup",
			expectImage: "quay.io/example/controller:v1",
		},
		{
			name:        "explicit selection among several",
			containers:  manager,
			inits:       []containerSpec{{name: "first", image: "a"}, {name: "second", image: "b"}},
			selection:   "second",
			expectInit:  "second",
			expectImage: "b",
		},
		{
			name:       "nonexistent selection",
			containers: manager,
			inits:      []containerSpec{{name: "first", image: "a"}},
			selection:  "missing",
			expectErr:  true,
			expectCat:  moduleerrors.ErrInvalidGenerateOptions,
		},
		{
			name:       "duplicate init names",
			containers: manager,
			inits:      []containerSpec{{name: "same", image: "a"}, {name: "same", image: "b"}},
			selection:  "same",
			expectErr:  true,
			expectCat:  moduleerrors.ErrInvalidDefinition,
		},
		{
			name:       "empty selected init image",
			containers: manager,
			inits:      []containerSpec{{name: "setup", image: ""}},
			selection:  "setup",
			expectErr:  true,
			expectCat:  moduleerrors.ErrInvalidDefinition,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)

			deployment := typedDeployment(t, controllerDeployment(t,
				"foo-controller", tc.containers, tc.inits))

			selection, err := discoverController(deployment, tc.selection)

			if tc.expectErr {
				g.Expect(err).To(HaveOccurred())
				g.Expect(errors.Is(err, tc.expectCat)).To(BeTrue(),
					"error %v must match category %v", err, tc.expectCat)

				return
			}

			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(selection.initName).To(Equal(tc.expectInit))
			g.Expect(selection.initImage).To(Equal(tc.expectImage))
		})
	}
}
