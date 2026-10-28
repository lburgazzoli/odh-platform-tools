// Package loader decodes and defaults local module definitions.
// Unknown fields are dropped; mapped fields with incompatible types fail.
package loader

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"

	fwresources "github.com/opendatahub-io/odh-platform-utilities/framework/resources"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

const definitionFailurePrefix = "invalid module definition: "

// Loader loads module definitions from a filesystem.
type Loader struct {
	fsys fs.FS
}

// New returns a loader reading definition files from fsys.
func New(fsys fs.FS) *Loader {
	return &Loader{fsys: fsys}
}

// Load decodes and defaults one definition from fsys. Input failures wrap
// ErrInvalidDefinition; context cancellation retains its identity.
func Load(
	ctx context.Context,
	fsys fs.FS,
	path string,
	opts ...LoadOption,
) (toolApi.PlatformModuleDefinition, error) {
	return New(fsys).Load(ctx, path, opts...)
}

// LoadFromPath accepts a relative or absolute filesystem path.
func LoadFromPath(
	ctx context.Context,
	path string,
	opts ...LoadOption,
) (toolApi.PlatformModuleDefinition, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return toolApi.PlatformModuleDefinition{}, moduleerrors.New(
			moduleerrors.ErrInvalidDefinition,
			definitionFailurePrefix+fmt.Sprintf("resolve definition file %s: %v", path, err),
			moduleerrors.WithCause(err))
	}

	relative, err := filepath.Rel("/", absolute)
	if err != nil {
		return toolApi.PlatformModuleDefinition{}, moduleerrors.New(
			moduleerrors.ErrInvalidDefinition,
			definitionFailurePrefix+fmt.Sprintf("resolve definition file %s: %v", path, err),
			moduleerrors.WithCause(err))
	}

	return Load(ctx, os.DirFS("/"), relative, opts...)
}

// Load decodes one document and applies placement defaults.
func (l *Loader) Load(
	ctx context.Context,
	path string,
	opts ...LoadOption,
) (toolApi.PlatformModuleDefinition, error) {
	options := LoadOptions{}

	for _, opt := range opts {
		if opt != nil {
			opt.ApplyTo(&options)
		}
	}

	if err := ctx.Err(); err != nil {
		return toolApi.PlatformModuleDefinition{}, err
	}

	data, err := l.readDefinitionFile(path)
	if err != nil {
		return toolApi.PlatformModuleDefinition{}, err
	}

	decoded, err := l.decodeDefinition(data)
	if err != nil {
		return toolApi.PlatformModuleDefinition{}, moduleerrors.New(
			moduleerrors.ErrInvalidDefinition,
			definitionFailurePrefix+fmt.Sprintf("%s: %v", path, err),
			moduleerrors.WithCause(err))
	}

	l.applyDefaults(&decoded)

	return decoded, nil
}

func (l *Loader) applyDefaults(definition *toolApi.PlatformModuleDefinition) {
	if definition.Spec.Instance.Name == "" {
		definition.Spec.Instance.Name = definition.Name
	}

	if definition.Spec.Controller.Deployment.Container == nil {
		definition.Spec.Controller.Deployment.Container = &toolApi.ContainerDefinition{
			Name: toolApi.DefaultMainContainerName,
		}

		return
	}

	if container := definition.Spec.Controller.Deployment.Container; container.Name == "" {
		container.Name = toolApi.DefaultMainContainerName
	}
}

func (l *Loader) readDefinitionFile(path string) ([]byte, error) {
	if path == "" {
		cause := errors.New("definition file path is required")
		return nil, moduleerrors.New(moduleerrors.ErrInvalidDefinition,
			definitionFailurePrefix+cause.Error(), moduleerrors.WithCause(cause))
	}

	data, err := fs.ReadFile(l.fsys, path)
	if err != nil {
		return nil, moduleerrors.New(moduleerrors.ErrInvalidDefinition,
			definitionFailurePrefix+fmt.Sprintf("read definition file %s: %v", path, err),
			moduleerrors.WithCause(err))
	}

	return data, nil
}

func (l *Loader) decodeDefinition(data []byte) (toolApi.PlatformModuleDefinition, error) {
	scheme := runtime.NewScheme()
	if err := toolApi.AddToScheme(scheme); err != nil {
		return toolApi.PlatformModuleDefinition{}, err
	}

	documents, err := fwresources.Decode(
		serializer.NewCodecFactory(scheme).UniversalDeserializer(), data)
	if err != nil {
		return toolApi.PlatformModuleDefinition{}, err
	}

	if len(documents) == 0 {
		return toolApi.PlatformModuleDefinition{}, errors.New("definition must contain one object document")
	}

	if len(documents) > 1 {
		return toolApi.PlatformModuleDefinition{}, errors.New("definition must contain exactly one document")
	}

	var definition toolApi.PlatformModuleDefinition
	if err := fwresources.ObjectFromUnstructured(scheme, &documents[0], &definition); err != nil {
		return toolApi.PlatformModuleDefinition{}, err
	}

	return definition, nil
}
