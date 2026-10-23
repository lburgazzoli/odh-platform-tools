// Package loader reads module definitions from explicit local files
// into typed, normalized definitions. It is a generic module service:
// it performs no environment lookup, rendering, linting, or cluster
// access.
package loader

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	jsonv2 "encoding/json/v2"

	yamlv2 "go.yaml.in/yaml/v2"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// Loader loads module definitions from a filesystem.
type Loader struct {
	fsys fs.FS
}

// New returns a loader reading definition files from fsys.
func New(fsys fs.FS) *Loader {
	return &Loader{fsys: fsys}
}

// Load reads, strictly decodes, and validates the module definition at
// path in fsys and returns it normalized: the one-shot form of
// New(fsys).Load.
//
// Every failure except cancellation is a configuration error matching
// errors.Is(err, moduleerrors.ErrInvalidDefinition). Cancellation and
// deadline errors are returned unwrapped so they keep their identity
// and precedence over configuration classification.
func Load(ctx context.Context, fsys fs.FS, path string) (v1alpha1.PlatformModuleDefinition, error) {
	return New(fsys).Load(ctx, path)
}

// LoadFromPath is Load over the operating-system filesystem: the path
// may be relative to the working directory or absolute.
func LoadFromPath(ctx context.Context, path string) (v1alpha1.PlatformModuleDefinition, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return v1alpha1.PlatformModuleDefinition{}, moduleerrors.NewDefinitionErrorf("resolve definition file %s: %w", path, err)
	}

	relative, err := filepath.Rel("/", absolute)
	if err != nil {
		return v1alpha1.PlatformModuleDefinition{}, moduleerrors.NewDefinitionErrorf("resolve definition file %s: %w", path, err)
	}

	return Load(ctx, os.DirFS("/"), relative)
}

// Load reads and strictly decodes the module definition at path in
// the loader's filesystem and returns it normalized.
//
// Every failure except cancellation is a configuration error matching
// errors.Is(err, moduleerrors.ErrInvalidDefinition). Cancellation and
// deadline errors are returned unwrapped so they keep their identity
// and precedence over configuration classification.
func (l *Loader) Load(ctx context.Context, path string) (v1alpha1.PlatformModuleDefinition, error) {
	if err := ctx.Err(); err != nil {
		return v1alpha1.PlatformModuleDefinition{}, err
	}

	data, err := l.readDefinitionFile(path)
	if err != nil {
		return v1alpha1.PlatformModuleDefinition{}, err
	}

	decoded, err := l.decodeDefinition(data)
	if err != nil {
		return v1alpha1.PlatformModuleDefinition{}, moduleerrors.NewDefinitionErrorf("%s: %w", path, err)
	}

	l.applyDefaults(&decoded)

	return decoded, nil
}

// applyDefaults applies the placement defaults to a freshly decoded
// definition in place: an omitted instance name becomes metadata.name,
// and an omitted main container name becomes
// v1alpha1.DefaultMainContainerName. The two defaults are independent:
// neither may skip the other. The definition belongs to the loader, so
// in-place mutation is safe.
func (l *Loader) applyDefaults(definition *v1alpha1.PlatformModuleDefinition) {
	if definition.Spec.Instance.Name == "" {
		definition.Spec.Instance.Name = definition.Name
	}

	container := definition.Spec.Controller.Deployment.Container
	if container == nil || container.Name != "" {
		return
	}

	container.Name = v1alpha1.DefaultMainContainerName
}

// readDefinitionFile reads the definition file at path in the loader's
// filesystem. Errors name the path exactly as passed to Load.
func (l *Loader) readDefinitionFile(path string) ([]byte, error) {
	if path == "" {
		return nil, moduleerrors.NewDefinitionError(errors.New("definition file path is required"))
	}

	data, err := fs.ReadFile(l.fsys, path)
	if err != nil {
		return nil, moduleerrors.NewDefinitionErrorf("read definition file %s: %w", path, err)
	}

	return data, nil
}

// decodeDefinition decodes the definition bytes in a single pass
// through the go.yaml.in/yaml/v2 machinery that the Kubernetes YAML
// conversion itself uses. The stream must hold exactly one nonempty
// document: further documents fail instead of being silently ignored.
// The document value is round-tripped through JSON for the strict
// typed decode, which rejects unknown members and name-case
// mismatches.
func (l *Loader) decodeDefinition(data []byte) (v1alpha1.PlatformModuleDefinition, error) {
	decoder := yamlv2.NewDecoder(bytes.NewReader(data))

	var document any
	for document == nil {
		if err := decoder.Decode(&document); err != nil {
			if errors.Is(err, io.EOF) {
				return v1alpha1.PlatformModuleDefinition{}, errors.New("definition must contain one object document")
			}
			return v1alpha1.PlatformModuleDefinition{}, err
		}
	}

	var extra any
	for {
		err := decoder.Decode(&extra)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return v1alpha1.PlatformModuleDefinition{}, err
		}
		if extra != nil {
			return v1alpha1.PlatformModuleDefinition{}, errors.New("definition must contain exactly one document")
		}
	}

	encoded, err := jsonv2.Marshal(document)
	if err != nil {
		return v1alpha1.PlatformModuleDefinition{}, err
	}

	var definition v1alpha1.PlatformModuleDefinition
	if err := jsonv2.Unmarshal(encoded, &definition, jsonv2.RejectUnknownMembers(true)); err != nil {
		return v1alpha1.PlatformModuleDefinition{}, err
	}

	return definition, nil
}
