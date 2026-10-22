package config

import (
	"errors"
	"fmt"
	"regexp"

	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ErrInvalidDefinition identifies configuration-level module definition
// failures: unreadable files, malformed documents, and structural
// violations. Errors returned by this package and by the loader wrap
// it, so callers classify them with errors.Is instead of matching
// message text.
var ErrInvalidDefinition = errors.New("invalid module definition")

// FieldError reports one structural problem at a definition field path
// such as spec.controller.deployment.container.image.
type FieldError struct {
	Path    string
	Message string
}

func (e *FieldError) Error() string {
	return e.Path + ": " + e.Message
}

// NewDefinitionError wraps err as a definition configuration failure.
// The cause stays reachable through errors.Is and errors.As, so
// cancellation and other sentinel identities survive wrapping.
func NewDefinitionError(err error) error {
	return fmt.Errorf("%w: %w", ErrInvalidDefinition, err)
}

// NewDefinitionErrorf is NewDefinitionError with fmt.Errorf-style
// formatting.
func NewDefinitionErrorf(format string, args ...any) error {
	return NewDefinitionError(fmt.Errorf(format, args...))
}

// UnmarshalJSONFrom decodes a container definition while distinguishing
// an omitted name from an explicit null or empty one: an omitted name
// leaves Name nil so placement defaulting applies, while null decodes
// like an empty string, which validation rejects for both placements.
func (c *ContainerDefinition) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	var wire struct {
		Name  jsontext.Value `json:"name,omitzero"`
		Image string         `json:"image"`
		Env   []EnvVar       `json:"env,omitzero"`
	}
	if err := jsonv2.UnmarshalDecode(dec, &wire, dec.Options()); err != nil {
		return err
	}

	name, err := decodeContainerName(wire.Name)
	if err != nil {
		return err
	}

	c.Name = name
	c.Image = wire.Image
	c.Env = wire.Env

	return nil
}

// decodeContainerName converts the raw name member into a pointer: nil
// for an omitted name, a pointer to the empty string for an explicit
// null (invalid exactly like an empty name), and a pointer to the
// decoded name otherwise.
func decodeContainerName(raw jsontext.Value) (*string, error) {
	switch {
	case len(raw) == 0:
		return nil, nil
	case string(raw) == "null":
		return new(""), nil
	default:
		var name string
		if err := jsonv2.Unmarshal(raw, &name); err != nil {
			return nil, err
		}
		return new(name), nil
	}
}

// UnmarshalJSONFrom decodes a module type from its definition wire
// format: lower-case group, version, and kind members. apimachinery
// stopped tagging those fields and the loader's JSON decoding is
// case-sensitive, so the wire form is defined here.
func (t *Type) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	var wire struct {
		Group   string `json:"group"`
		Version string `json:"version"`
		Kind    string `json:"kind"`
	}
	if err := jsonv2.UnmarshalDecode(dec, &wire, dec.Options()); err != nil {
		return err
	}

	*t = Type{GroupVersionKind: schema.GroupVersionKind{
		Group:   wire.Group,
		Version: wire.Version,
		Kind:    wire.Kind,
	}}

	return nil
}

// MarshalJSONTo encodes a module type in its definition wire format.
func (t Type) MarshalJSONTo(enc *jsontext.Encoder) error {
	return jsonv2.MarshalEncode(enc, &struct {
		Group   string `json:"group"`
		Version string `json:"version"`
		Kind    string `json:"kind"`
	}{
		Group:   t.Group,
		Version: t.Version,
		Kind:    t.Kind,
	})
}

// problems collects independent field errors during validation.
type problems []error

// add records a problem at a definition field path.
func (p *problems) add(path, format string, args ...any) {
	*p = append(*p, &FieldError{Path: path, Message: fmt.Sprintf(format, args...)})
}

// err returns the aggregated definition error, or nil when no problem
// was recorded.
func (p problems) err() error {
	if len(p) == 0 {
		return nil
	}
	return NewDefinitionError(errors.Join(p...))
}

// Normalize validates d and returns a copy with placement defaults
// applied: an omitted main container name becomes
// DefaultMainContainerName. The receiver is not mutated.
//
// On failure it returns an error matching errors.Is(err,
// ErrInvalidDefinition) that aggregates one FieldError per independent
// problem.
func (d PlatformModuleDefinition) Normalize() (PlatformModuleDefinition, error) {
	var p problems
	d.validate(&p)

	if err := p.err(); err != nil {
		return PlatformModuleDefinition{}, err
	}

	d.applyDefaults()

	return d, nil
}

// applyDefaults defaults the main container name in place. The
// container is copied first because the value receiver of Normalize
// shares pointer fields with the caller's definition.
func (d *PlatformModuleDefinition) applyDefaults() {
	deployment := &d.Spec.Controller.Deployment
	if deployment.Container == nil || deployment.Container.Name != nil {
		return
	}

	container := *deployment.Container
	container.Name = new(DefaultMainContainerName)
	deployment.Container = &container
}

// validate checks the envelope and delegates to the spec sections.
func (d *PlatformModuleDefinition) validate(p *problems) {
	if d.APIVersion != APIVersion {
		p.add("apiVersion", "must be %q", APIVersion)
	}
	if d.Kind != Kind {
		p.add("kind", "must be %q", Kind)
	}

	if d.Metadata.Name == "" {
		p.add("metadata.name", "name is required")
	} else if d.Spec.Manifests.Format == FormatHelm {
		d.validateHelmReleaseName(p)
	}

	d.Spec.validate(p)
}

// helmReleaseNamePattern is the release name constraint the Helm
// renderer enforces; metadata.name becomes the chart release name.
var helmReleaseNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// maxHelmReleaseNameLength is the Helm release name length limit.
const maxHelmReleaseNameLength = 53

// validateHelmReleaseName applies the Helm release name constraints to
// metadata.name when the Helm backend is selected. The Helm renderer
// derives the release name from it instead of the definition carrying a
// separate one.
func (d *PlatformModuleDefinition) validateHelmReleaseName(p *problems) {
	name := d.Metadata.Name
	switch {
	case len(name) > maxHelmReleaseNameLength:
		p.add("metadata.name", "must be at most %d characters to be usable as a Helm release name", maxHelmReleaseNameLength)
	case !helmReleaseNamePattern.MatchString(name):
		p.add("metadata.name", "must match %s to be usable as a Helm release name", helmReleaseNamePattern)
	}
}

// validate checks the spec sections and their cross-field rules.
func (s *Spec) validate(p *problems) {
	validateType(s.Type.GroupVersionKind, p)
	s.Manifests.validate(p)
	s.Controller.Deployment.validate(p)
	s.validateRelatedImages(p)
}

// validateType checks that the module type names a complete group,
// version, and kind.
func validateType(t schema.GroupVersionKind, p *problems) {
	if t.Group == "" {
		p.add("spec.type.group", "group is required")
	}
	if t.Version == "" {
		p.add("spec.type.version", "version is required")
	}
	if t.Kind == "" {
		p.add("spec.type.kind", "kind is required")
	}
}

// validate checks that exactly the backend block matching format is
// present, with a nonempty path.
func (m *Manifests) validate(p *problems) {
	switch m.Format {
	case FormatHelm:
		if m.Kustomize != nil {
			p.add("spec.manifests.kustomize", "must not be set when format is %q", FormatHelm)
		}
		if m.Helm == nil {
			p.add("spec.manifests.helm", "is required when format is %q", FormatHelm)
		} else if m.Helm.Path == "" {
			p.add("spec.manifests.helm.path", "path is required")
		}
	case FormatKustomize:
		if m.Helm != nil {
			p.add("spec.manifests.helm", "must not be set when format is %q", FormatKustomize)
		}
		if m.Kustomize == nil {
			p.add("spec.manifests.kustomize", "is required when format is %q", FormatKustomize)
		} else if m.Kustomize.Path == "" {
			p.add("spec.manifests.kustomize.path", "path is required")
		}
	default:
		p.add("spec.manifests.format", "must be %q or %q", FormatHelm, FormatKustomize)
	}
}

// validate checks the deployment identity and the selected containers.
func (d *Deployment) validate(p *problems) {
	if d.Name == "" {
		p.add("spec.controller.deployment.name", "name is required")
	}

	if d.Container == nil {
		p.add("spec.controller.deployment.container", "container is required")
	} else {
		if d.Container.Name != nil && *d.Container.Name == "" {
			p.add("spec.controller.deployment.container.name", "must not be empty; omit it to use %q", DefaultMainContainerName)
		}
		d.Container.validate(p, "spec.controller.deployment.container")
	}

	// The main container name may be omitted and defaulted; an init
	// container always needs an explicit name.
	if d.InitContainer != nil {
		if d.InitContainer.Name == nil || *d.InitContainer.Name == "" {
			p.add("spec.controller.deployment.initContainer.name", "is required for an init container")
		}
		d.InitContainer.validate(p, "spec.controller.deployment.initContainer")
	}
}

// validate checks the placement-independent container rules: the
// required image and the explicit env entries. Name rules are
// placement-specific and belong to Deployment.validate.
func (c *ContainerDefinition) validate(p *problems, path string) {
	if c.Image == "" {
		p.add(path+".image", "image is required")
	}

	seen := make(map[string]struct{}, len(c.Env))
	for i, env := range c.Env {
		envPath := fmt.Sprintf("%s.env[%d]", path, i)

		if env.Name == "" {
			p.add(envPath+".name", "name is required")
			continue
		}
		if _, duplicate := seen[env.Name]; duplicate {
			p.add(envPath+".name", "duplicate environment variable %q", env.Name)
			continue
		}
		seen[env.Name] = struct{}{}

		if env.Value == nil {
			p.add(envPath+".value", "value is required; use an explicit empty string for an empty value")
		}
	}
}

// validateRelatedImages checks that the declared related image names
// are portable, unique, and do not collide with explicit container env
// names: a future runner injects them into both selected containers.
func (s *Spec) validateRelatedImages(p *problems) {
	if len(s.RelatedImages) == 0 {
		return
	}

	explicit := s.Controller.Deployment.explicitEnvNames()

	seen := make(map[string]struct{}, len(s.RelatedImages))
	for i, name := range s.RelatedImages {
		path := fmt.Sprintf("spec.relatedImages[%d]", i)

		if !relatedImageNamePattern.MatchString(name) {
			p.add(path, "must be a portable environment variable name matching %s", relatedImageNamePattern)
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			p.add(path, "duplicate related image %q", name)
			continue
		}
		seen[name] = struct{}{}

		if _, collides := explicit[name]; collides {
			p.add(path, "collides with an explicit container environment variable %q", name)
		}
	}
}

// relatedImageNamePattern is a portable environment variable name: a
// future runner reads these from the process environment.
var relatedImageNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// explicitEnvNames returns the env var names declared by both selected
// containers.
func (d *Deployment) explicitEnvNames() map[string]struct{} {
	names := make(map[string]struct{})

	for _, container := range []*ContainerDefinition{d.Container, d.InitContainer} {
		if container == nil {
			continue
		}
		for _, env := range container.Env {
			names[env.Name] = struct{}{}
		}
	}

	return names
}
