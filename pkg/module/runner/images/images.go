// Package images resolves the image sources a module run needs: the
// explicitly requested related-image keys, snapshotted from a
// dotenv-compatible file and the process environment with
// environment-over-file precedence, and the shared whole-token image
// contract used by the runner and the adaptation layer.
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
	"strconv"

	"github.com/spf13/viper"
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
			return nil, newImageSourceError(path, err)
		}
	}

	file := viper.New()
	if path != "" {
		file.SetConfigFile(path)
		file.SetConfigType("env")

		if err := file.ReadInConfig(); err != nil {
			return nil, newImageSourceError(path, err)
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

// ImageSourceError reports a related-images input failure: the
// selected file is unreadable or malformed. Its Error text names the
// file but never its content, so dotenv values are not echoed in
// command output; the original cause stays reachable through Unwrap
// for callers that need the details.
type ImageSourceError struct {
	path  string
	cause error
}

func newImageSourceError(path string, cause error) *ImageSourceError {
	return &ImageSourceError{path: path, cause: cause}
}

// Error implements error with safe, content-free text.
func (e *ImageSourceError) Error() string {
	return "invalid related images file " + e.path
}

// Unwrap exposes the underlying cause, which may quote file content.
func (e *ImageSourceError) Unwrap() error {
	return e.cause
}
