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

// Package source resolves authored manifest source entries against the
// definition file that declared them. It is the leaf both rendering
// backends consume, so the parent rendering service can import those
// backends without an import cycle.
package source

import "path/filepath"

// ResolveEntry resolves an authored manifest path against the
// definition file's directory. An empty path — an omitted block or an
// omitted path — resolves to the definition file's directory itself;
// the path is otherwise used exactly as configured, and an absolute
// path is honored as is. Existence and shape of the entry are the
// render engines' to validate, so a missing or unsuitable entry fails
// at render time.
func ResolveEntry(root string, authored string) string {
	if filepath.IsAbs(authored) {
		return filepath.Clean(authored)
	}

	return filepath.Join(root, authored)
}
