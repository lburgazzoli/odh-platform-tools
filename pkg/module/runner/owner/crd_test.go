package owner

import (
	"encoding/json"
	"testing"

	. "github.com/onsi/gomega"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/lburgazzoli/gomega-matchers/pkg/matchers/jq"

	"github.com/lburgazzoli/odh-platform-tools/config/crd"
	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
)

// embeddedCRD decodes the CRD embedded in the binary and marks it
// served: established with its names accepted, carrying the server
// identity a live CRD would have.
func embeddedCRD(t *testing.T, g *WithT) *apiextensionsv1.CustomResourceDefinition {
	t.Helper()

	live := &apiextensionsv1.CustomResourceDefinition{}
	g.Expect(yaml.Unmarshal(crd.PlatformModuleDefinitionYAML, live)).To(Succeed())
	g.Expect(live.Name).To(Equal(v1alpha1.CRDName))

	live.UID = "crd-uid"
	live.ResourceVersion = "12"
	live.Status.Conditions = []apiextensionsv1.CustomResourceDefinitionCondition{
		{Type: apiextensionsv1.Established, Status: apiextensionsv1.ConditionTrue},
		{Type: apiextensionsv1.NamesAccepted, Status: apiextensionsv1.ConditionTrue},
	}

	return live
}

// seededCRDJSON encodes a live CRD as the server-side JSON body.
func seededCRDJSON(t *testing.T, g *WithT, crd *apiextensionsv1.CustomResourceDefinition) string {
	t.Helper()

	encoded, err := json.Marshal(crd)
	g.Expect(err).To(Succeed())

	return string(encoded)
}

// TestEnsureCreatesEmbeddedCRD proves a server without the definition
// CRD gets the embedded generated one created — once — and the
// definition publishes behind it.
func TestEnsureCreatesEmbeddedCRD(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	server := newFakeOwnerServer(t)

	owner, err := ensured(t.Context(), t, g, server, newAuthoredDefinition())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(owner.UID).To(Equal(types.UID(definitionUID)))
	g.Expect(server.crdWriteCount()).To(Equal(1))

	stored := server.storedCRD(t, v1alpha1.CRDName)
	g.Expect(stored).To(SatisfyAll(
		jq.Match(`.metadata.name == "platformmoduledefinitions.config.opendatahub.io"`),
		jq.Match(`.spec.group == "config.opendatahub.io"`),
		jq.Match(`.spec.scope == "Cluster"`),
		jq.Match(`.spec.names.plural == "platformmoduledefinitions"`),
		jq.Match(`.status.conditions[0].type == "Established"`),
	))
}

// TestEnsureReusesCompatibleCRDWithoutRewriting proves a live CRD the
// API server has Kubernetes-defaulted — a None conversion strategy
// and regenerated schema description prose — is reused semantically
// without any rewrite: documentation drift alone is never an
// incompatibility.
func TestEnsureReusesCompatibleCRDWithoutRewriting(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	server := newFakeOwnerServer(t)

	live := embeddedCRD(t, g)
	live.Spec.Conversion = &apiextensionsv1.CustomResourceConversion{
		Strategy: apiextensionsv1.NoneConverter,
	}
	specSchema := live.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
	specSchema.Description = "regenerated prose from a newer controller-gen run"
	live.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"] = specSchema
	server.seedCRD(t, seededCRDJSON(t, g, live))

	owner, err := ensured(t.Context(), t, g, server, newAuthoredDefinition())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(owner.UID).To(Equal(types.UID(definitionUID)))
	g.Expect(server.crdWriteCount()).To(Equal(0))
	g.Expect(server.definitionWriteCount()).To(Equal(1))
}

// TestEnsureRejectsUnusableCRDs proves a live CRD this run must not
// build on — terminating, owner-referenced, or semantically
// incompatible — fails the run before any definition write, without
// taking the schema over.
func TestEnsureRejectsUnusableCRDs(t *testing.T) {
	t.Parallel()

	cases := map[string]func(live *apiextensionsv1.CustomResourceDefinition){
		"terminating": func(live *apiextensionsv1.CustomResourceDefinition) {
			now := metav1.Now()
			live.DeletionTimestamp = &now
		},
		"owner referenced": func(live *apiextensionsv1.CustomResourceDefinition) {
			live.OwnerReferences = []metav1.OwnerReference{{
				APIVersion: "v1", Kind: "Pod", Name: "foreign", UID: "foreign-uid",
			}}
		},
		"incompatible scope": func(live *apiextensionsv1.CustomResourceDefinition) {
			live.Spec.Scope = apiextensionsv1.NamespaceScoped
		},
		"incompatible schema": func(live *apiextensionsv1.CustomResourceDefinition) {
			spec := live.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
			delete(spec.Properties, "controller")
			live.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"] = spec
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			g := NewWithT(t)
			server := newFakeOwnerServer(t)

			live := embeddedCRD(t, g)
			mutate(live)
			seeded := seededCRDJSON(t, g, live)
			server.seedCRD(t, seeded)

			owner, err := ensured(t.Context(), t, g, server, newAuthoredDefinition())

			g.Expect(owner).To(BeNil())

			typed := ownerError(g, err)
			g.Expect(typed.Outcome).To(Equal(OutcomeNotAttempted))
			g.Expect(typed.Owner).To(BeNil())

			g.Expect(server.crdWriteCount()).To(Equal(0))
			g.Expect(server.requestCount()).To(Equal(1))

			// The live CRD was never rewritten.
			g.Expect(server.storedCRD(t, v1alpha1.CRDName)).To(BeIdenticalTo(seeded))
		})
	}
}

// TestEnsureRacedCRDCreationIsChecked proves a CRD creation raced by
// another writer falls back to checking theirs: the winner is reused
// when compatible, never duplicated or overwritten.
func TestEnsureRacedCRDCreationIsChecked(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	server := newFakeOwnerServer(t)

	live := embeddedCRD(t, g)
	live.Spec.Conversion = &apiextensionsv1.CustomResourceConversion{
		Strategy: apiextensionsv1.NoneConverter,
	}

	// The race: the CRD read reports it missing, and the creation is
	// refused because another writer won it.
	server.raceCRDCreation = seededCRDJSON(t, g, live)

	owner, err := ensured(t.Context(), t, g, server, newAuthoredDefinition())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(owner.UID).To(Equal(types.UID(definitionUID)))
	g.Expect(server.crdWriteCount()).To(Equal(0))
	g.Expect(server.definitionWriteCount()).To(Equal(1))
}
