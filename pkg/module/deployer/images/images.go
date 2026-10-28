// Package images resolves the image sources a module deploy needs: the
// explicitly requested related-image keys, snapshotted from a
// dotenv-compatible file and the process environment with
// environment-over-file precedence, and the shared whole-token image
// contract used by the deployer and the adaptation layer.
//
// Resolution uses private Viper state only: no implicit file
// discovery, no global Viper, no watch mode, and no writes to the
// process environment. Requested keys are looked up directly, never
// enumerated, so unused file or ambient entries are never returned.
// File parsing is Viper's native dotenv codec: quoting, comments, and
// variable expansion keep their upstream semantics — double-quoted
// values may expand other variables, including ambient process
// variables, before this package ever sees them. This package neither
// adds expansion nor promises isolation from it.
package images

import (
	"regexp"
	"strconv"

	"github.com/spf13/viper"

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// Load snapshots the requested keys from the process environment and,
// when path is non-empty, the dotenv file at path, resolved from the
// working directory regardless of the file-name extension. An empty
// path means environment-only resolution; an explicit but unreadable
// or malformed file is an input error.
//
// Precedence is environment over file. A key whose environment
// variable is explicitly empty masks any file value and counts as
// missing, matching the operator's rule that empty image sources keep
// rendered defaults. Environment bindings are exact and
// case-sensitive; file keys follow Viper's case-insensitive rules.
// The result carries the authored key spelling and only nonempty
// values: absent and masked keys are simply omitted.
func Load(path string, keys []string) (map[string]string, error) {
	env := viper.New()
	env.AllowEmptyEnv(true)

	// Requested keys bind to per-index internal lookup keys so that
	// case-colliding requests such as FOO and foo never alias and the
	// result never depends on the enumeration order. Only the bound
	// internal keys are ever read from this instance.
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

		// A bound variable that is unset makes Get return nil; an
		// explicitly empty variable returns the empty string and
		// masks the file value.
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

// imageTokenPattern matches a complete {{NAME}} image reference with
// a portable environment variable name — an upper- or lower-case
// letter or underscore, then letters, digits, or underscores —
// spanning the whole value.
var imageTokenPattern = regexp.MustCompile(`^\{\{([A-Za-z_][A-Za-z0-9_]*)\}\}$`)

// ImageSourceKey reports whether image is a complete {{NAME}} token
// and returns NAME. Tokens are matched whole: partial references,
// surrounding text, inner whitespace, and non-portable names are not
// tokens.
func ImageSourceKey(image string) (string, bool) {
	match := imageTokenPattern.FindStringSubmatch(image)
	if match == nil {
		return "", false
	}

	return match[1], true
}

// ResolveImage applies the shared image contract to a configured
// image value. A complete {{NAME}} token resolves to the value of
// NAME in values; a missing or empty value resolves to the empty
// string, which callers treat as retaining the rendered default. Any
// other value is a literal image override, returned as authored —
// including the empty string. Resolution is whole-token only and
// never recursive: a resolved value is used verbatim even when it
// itself looks like a token.
func ResolveImage(image string, values map[string]string) string {
	key, ok := ImageSourceKey(image)
	if !ok {
		return image
	}

	return values[key]
}
