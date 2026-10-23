package images

import "regexp"

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
