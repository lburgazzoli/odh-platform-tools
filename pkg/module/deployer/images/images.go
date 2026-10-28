// Package images resolves requested image sources from dotenv files and the
// environment. Viper's dotenv parser may expand ambient variables in quoted values.
package images

import (
	"regexp"
	"strconv"

	"github.com/spf13/viper"

	toolApi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// Load resolves only the requested keys. Environment values override the
// optional dotenv file; explicitly empty values mask file values and are omitted.
// Environment names are case-sensitive; file keys follow Viper's case folding.
func Load(path string, keys []string) (map[string]string, error) {
	env := viper.New()
	env.AllowEmptyEnv(true)

	// Use indexed lookup keys so case-distinct environment names cannot alias.
	for i, key := range keys {
		if err := env.BindEnv(strconv.Itoa(i), key); err != nil {
			return nil, moduleerrors.New(
				moduleerrors.ErrImageSource,
				"invalid related images file "+path,
				moduleerrors.WithCause(err),
			)
		}
	}

	file := viper.New()
	if path != "" {
		file.SetConfigFile(path)
		file.SetConfigType("env")

		if err := file.ReadInConfig(); err != nil {
			return nil, moduleerrors.New(
				moduleerrors.ErrImageSource,
				"invalid related images file "+path,
				moduleerrors.WithCause(err),
			)
		}
	}

	values := make(map[string]string, len(keys))
	for i, key := range keys {
		value := ""

		// An empty environment value masks the file; an unset one falls back.
		if bound, ok := env.Get(strconv.Itoa(i)).(string); ok {
			value = bound
		} else if fromFile, ok := file.Get(key).(string); ok {
			value = fromFile
		}

		if value != "" {
			values[key] = value
		}
	}

	return values, nil
}

// Snapshot resolves related images and tokens in both controller image fields.
// Only RelatedImages controls which values are injected as environment variables.
func Snapshot(path string, definition toolApi.PlatformModuleDefinition) (map[string]string, error) {
	keys := make([]string, 0, len(definition.Spec.RelatedImages)+2)
	keys = append(keys, definition.Spec.RelatedImages...)

	if container := definition.Spec.Controller.Deployment.Container; container != nil {
		if key, ok := ImageSourceKey(container.Image); ok {
			keys = append(keys, key)
		}
	}
	if init := definition.Spec.Controller.Deployment.InitContainer; init != nil {
		if key, ok := ImageSourceKey(init.Image); ok {
			keys = append(keys, key)
		}
	}

	return Load(path, keys)
}

var imageTokenPattern = regexp.MustCompile(`^\{\{([A-Za-z_][A-Za-z0-9_]*)\}\}$`)

// ImageSourceKey extracts a portable variable name from a whole {{NAME}} token.
func ImageSourceKey(image string) (string, bool) {
	match := imageTokenPattern.FindStringSubmatch(image)
	if match == nil {
		return "", false
	}

	return match[1], true
}

// ResolveImage expands a whole {{NAME}} token once; other values stay literal.
// Missing or empty sources return an empty string.
func ResolveImage(image string, values map[string]string) string {
	key, ok := ImageSourceKey(image)
	if !ok {
		return image
	}

	return values[key]
}
