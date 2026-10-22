package config

import (
	"errors"
	"fmt"

	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
)

// ErrInvalidDefinition identifies configuration-level module definition
// failures: unreadable files, malformed documents, and structural
// violations. Errors returned by this package and by the loader wrap
// it, so callers classify them with errors.Is instead of matching
// message text.
var ErrInvalidDefinition = errors.New("invalid module definition")

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

	*t = Type{
		Group:   wire.Group,
		Version: wire.Version,
		Kind:    wire.Kind}

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

// Normalize returns a copy of d with placement defaults applied: an
// omitted instance name becomes metadata.name, and an omitted main
// container name becomes DefaultMainContainerName. The receiver is not
// mutated, and normalization is idempotent.
//
// Structural validation is deliberately deferred: strict decoding
// rejects unknown members and type mismatches, and JSON Schema
// validation of the remaining structure will be added later.
func (d PlatformModuleDefinition) Normalize() PlatformModuleDefinition {
	d.applyDefaults()

	return d
}

// applyDefaults defaults the empty instance and main container names
// in place. The two defaults are independent: neither may skip the
// other. The receiver already belongs to Normalize's copy.
func (d *PlatformModuleDefinition) applyDefaults() {
	d.Spec.Instance.applyDefaultName(d.Metadata.Name)
	d.applyDefaultContainerName()
}

// applyDefaultName fills an empty instance name with the fallback
// metadata name in place. An explicit name is never overwritten.
func (i *Instance) applyDefaultName(fallbackName string) {
	if i.Name == "" {
		i.Name = fallbackName
	}
}

// applyDefaultContainerName defaults an empty main container name in
// place. The container is copied first because the value receiver of
// Normalize shares pointer fields with the caller's definition.
func (d *PlatformModuleDefinition) applyDefaultContainerName() {
	deployment := &d.Spec.Controller.Deployment
	if deployment.Container == nil || deployment.Container.Name != "" {
		return
	}

	container := *deployment.Container
	container.Name = DefaultMainContainerName
	deployment.Container = &container
}
