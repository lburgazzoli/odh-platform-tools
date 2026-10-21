package renderer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// kustomizationFieldPolicy classifies the fields of a kustomization
// document as the supported Kustomize versions define them. Every field
// absent from the table is rejected: unknown fields fail closed because
// future Kustomize capabilities may be fetch-capable.
type kustomizationFieldPolicy int

const (
	// Structural fields carry no file references and no execution.
	policyStructural kustomizationFieldPolicy = iota

	// Path lists hold strings referencing local files or directories.
	policyPathList

	// Patch lists hold objects whose optional path key references a
	// local file (patches, patchesJson6902, replacements).
	policyPatchList

	// Generator lists hold builtin generator objects whose envs and
	// files keys reference local files.
	policyGeneratorList

	// Control lists hold local control files whose content must itself
	// be checked for external or exec plugins.
	policyControlList

	// Rejected fields are fetch-capable or otherwise out of offline
	// policy.
	policyRejected
)

// kustomizationFields maps every known kustomization field to its
// policy.
var kustomizationFields = map[string]kustomizationFieldPolicy{
	// Structural.
	"apiVersion":        policyStructural,
	"kind":              policyStructural,
	"metadata":          policyStructural,
	"namespace":         policyStructural,
	"namePrefix":        policyStructural,
	"nameSuffix":        policyStructural,
	"commonLabels":      policyStructural,
	"labels":            policyStructural,
	"commonAnnotations": policyStructural,
	"images":            policyStructural,
	"imageTags":         policyStructural,
	"replicas":          policyStructural,
	"vars":              policyStructural,
	"sortOptions":       policyStructural,
	"generatorOptions":  policyStructural,
	"buildMetadata":     policyStructural,

	// Local path lists.
	"resources":             policyPathList,
	"components":            policyPathList,
	"bases":                 policyPathList,
	"crds":                  policyPathList,
	"patchesStrategicMerge": policyPathList,
	"configurations":        policyPathList,

	// Control files with content checks.
	"generators":   policyControlList,
	"transformers": policyControlList,
	"validators":   policyControlList,

	// Objects with nested path fields.
	"patches":            policyPatchList,
	"patchesJson6902":    policyPatchList,
	"replacements":       policyPatchList,
	"configMapGenerator": policyGeneratorList,
	"secretGenerator":    policyGeneratorList,

	// Fetch-capable or out of offline policy.
	"openapi":                     policyRejected,
	"helmGlobals":                 policyRejected,
	"helmCharts":                  policyRejected,
	"helmChartInflationGenerator": policyRejected,
}

// directoryReferenceFields are path-list fields whose entries may point
// at kustomization directories instead of plain files.
var directoryReferenceFields = map[string]struct{}{
	"resources":  {},
	"components": {},
	"bases":      {},
}

// referenceKind selects how a kustomization reference is captured.
type referenceKind int

const (
	// refDirectory entries may be files or kustomization directories.
	refDirectory referenceKind = iota

	// refFile entries must be regular files.
	refFile

	// refControl entries are files whose content is plugin-checked.
	refControl
)

// kustomizeReference is one local input referenced by a kustomization.
type kustomizeReference struct {
	value string
	kind  referenceKind
}

// captureKustomizationDir captures the kustomization rooted at dir and,
// recursively, every local input it references. References are
// discovered by parsing the captured kustomization bytes, so the policy
// always describes the bytes that are actually snapshotted, and
// originals cannot change what gets captured after the fact.
func captureKustomizationDir(state *captureState, dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return unsafeError("missing kustomization directory %s", dir)
		}

		return fmt.Errorf("stat %s: %w", dir, err)
	}

	// A kustomization reached through a contained symlink keeps the
	// link in the snapshot so parent references still resolve.
	if info.Mode()&os.ModeSymlink != 0 {
		if err := captureLink(state, dir); err != nil {
			return err
		}
	}

	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return unsafeError("unresolvable kustomization directory %s", dir)
	}

	if _, err := relativeContainedPath(canonical, state.moduleRoot); err != nil {
		return err
	}

	if _, seen := state.visitedDirs[canonical]; seen {
		return nil
	}
	state.visitedDirs[canonical] = struct{}{}

	for _, name := range []string{"kustomization.yaml", "kustomization.yml"} {
		entryPath := filepath.Join(canonical, name)

		entryInfo, err := os.Lstat(entryPath)
		if err != nil {
			continue
		}

		if entryInfo.Mode()&os.ModeSymlink != 0 {
			return captureKustomizationEntry(state, canonical, entryPath)
		}

		if entryInfo.Mode().IsRegular() {
			return captureKustomizationEntry(state, canonical, entryPath)
		}
	}

	return unsafeError("no kustomization.yaml in %s", canonical)
}

// captureKustomizationEntry captures one kustomization file, validates
// its policy, and captures everything it references.
func captureKustomizationEntry(state *captureState, canonicalDir, entryPath string) error {
	if info, err := os.Lstat(entryPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if err := captureSymlinkedFile(state, entryPath); err != nil {
			return err
		}
	} else if _, err := captureRegular(state, entryPath); err != nil {
		return err
	}

	resolved, err := filepath.EvalSymlinks(entryPath)
	if err != nil {
		return unsafeError("unresolvable kustomization file %s", entryPath)
	}

	file, err := captureRegular(state, resolved)
	if err != nil {
		return err
	}

	location := relativeDisplayPath(state, file.Path)

	kustomization, err := decodeYAMLDocument(file.Bytes)
	if err != nil {
		return unsafeError("invalid kustomization %s: %v", location, err)
	}

	refs, err := kustomizationReferences(location, kustomization)
	if err != nil {
		return err
	}

	for _, ref := range refs {
		if reason, unsafe := unsafeReferenceReason(ref.value); unsafe {
			return unsafeError("kustomization %s references %q: %s", location, ref.value, reason)
		}

		target := filepath.Join(canonicalDir, ref.value)

		switch ref.kind {
		case refDirectory:
			if err := captureDirectoryReference(state, target); err != nil {
				return err
			}
		case refFile:
			if err := captureReferenceFile(state, target); err != nil {
				return err
			}
		case refControl:
			if err := captureControlReference(state, target); err != nil {
				return err
			}
		}
	}

	return nil
}

// captureDirectoryReference captures a resource, component, or base
// entry: kustomization directories recurse, plain files capture.
func captureDirectoryReference(state *captureState, target string) error {
	info, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return unsafeError("missing input %s", target)
		}

		return fmt.Errorf("stat %s: %w", target, err)
	}

	switch {
	case info.IsDir():
		return captureKustomizationDir(state, target)
	case info.Mode()&os.ModeSymlink != 0:
		resolved, err := filepath.EvalSymlinks(target)
		if err != nil {
			return unsafeError("broken symlink %s", target)
		}

		resolvedInfo, err := os.Stat(resolved)
		if err != nil {
			return fmt.Errorf("stat %s: %w", resolved, err)
		}

		if resolvedInfo.IsDir() {
			return captureKustomizationDir(state, target)
		}

		return captureSymlinkedFile(state, target)
	case info.Mode().IsRegular():
		_, err := captureRegular(state, target)
		return err
	default:
		return unsafeError("reference %s is not a regular file", target)
	}
}

// captureControlReference captures a generator, transformer, or
// validator control file and validates that its content is not an
// external or exec plugin.
func captureControlReference(state *captureState, target string) error {
	if err := captureReferenceFile(state, target); err != nil {
		return err
	}

	info, err := os.Lstat(target)
	if err != nil {
		return fmt.Errorf("stat %s: %w", target, err)
	}

	resolved := target
	if info.Mode()&os.ModeSymlink != 0 {
		if resolved, err = filepath.EvalSymlinks(target); err != nil {
			return unsafeError("broken symlink %s", target)
		}
	}

	file, err := captureRegular(state, resolved)
	if err != nil {
		return err
	}

	location := relativeDisplayPath(state, file.Path)

	document, err := decodeYAMLDocument(file.Bytes)
	if err != nil {
		return unsafeError("invalid control file %s: %v", location, err)
	}

	apiVersion, present := document["apiVersion"]
	if !present {
		// Plain configuration data, such as transformer configurations.
		return nil
	}

	version, ok := apiVersion.(string)
	if !ok {
		return unsafeError("control file %s has a malformed apiVersion", location)
	}

	if version != "builtin" && !strings.HasPrefix(version, "kustomize.config.k8s.io/") {
		return unsafeError("control file %s uses external or exec plugin %q", location, version)
	}

	return nil
}

// kustomizationReferences validates the field policy of a captured
// kustomization and returns the local inputs it references.
func kustomizationReferences(location string, kustomization map[string]any) ([]kustomizeReference, error) {
	var refs []kustomizeReference

	appendStringList := func(field string, value any, kind referenceKind) error {
		items, ok := value.([]any)
		if !ok {
			return unsafeError("kustomization field %q in %s must be a list", field, location)
		}

		for _, item := range items {
			entry, ok := item.(string)
			if !ok {
				return unsafeError("kustomization field %q in %s must contain strings", field, location)
			}

			refs = append(refs, kustomizeReference{value: entry, kind: kind})
		}

		return nil
	}

	appendPathField := func(field string, value any) error {
		items, ok := value.([]any)
		if !ok {
			return unsafeError("kustomization field %q in %s must be a list", field, location)
		}

		for _, item := range items {
			// Patch entries accept a plain path string or an object
			// with an optional path key.
			if entry, ok := item.(string); ok {
				refs = append(refs, kustomizeReference{value: entry, kind: refFile})

				continue
			}

			entry, ok := item.(map[string]any)
			if !ok {
				return unsafeError("kustomization field %q in %s must contain objects or paths", field, location)
			}

			pathValue, present := entry["path"]
			if !present {
				continue
			}

			path, ok := pathValue.(string)
			if !ok {
				return unsafeError("kustomization field %q in %s has a malformed path", field, location)
			}

			refs = append(refs, kustomizeReference{value: path, kind: refFile})
		}

		return nil
	}

	appendGeneratorList := func(field string, value any) error {
		items, ok := value.([]any)
		if !ok {
			return unsafeError("kustomization field %q in %s must be a list", field, location)
		}

		for _, item := range items {
			entry, ok := item.(map[string]any)
			if !ok {
				return unsafeError("kustomization field %q in %s must contain objects", field, location)
			}

			for _, filesField := range []string{"envs", "files"} {
				list, present := entry[filesField]
				if !present {
					continue
				}

				if err := appendStringList(field+"."+filesField, list, refFile); err != nil {
					return err
				}
			}
		}

		return nil
	}

	for field, value := range kustomization {
		policy, known := kustomizationFields[field]
		if !known {
			return nil, unsafeError("kustomization %s uses unknown field %q (fail closed)", location, field)
		}

		switch policy {
		case policyStructural:
			continue
		case policyRejected:
			return nil, unsafeError("kustomization %s uses field %q, which is not allowed offline", location, field)
		case policyPathList:
			kind := refFile
			if _, dirCapable := directoryReferenceFields[field]; dirCapable {
				kind = refDirectory
			}

			if err := appendStringList(field, value, kind); err != nil {
				return nil, err
			}
		case policyControlList:
			if err := appendStringList(field, value, refControl); err != nil {
				return nil, err
			}
		case policyPatchList:
			if err := appendPathField(field, value); err != nil {
				return nil, err
			}
		case policyGeneratorList:
			if err := appendGeneratorList(field, value); err != nil {
				return nil, err
			}
		}
	}

	return refs, nil
}

// relativeDisplayPath renders a captured path for error messages.
func relativeDisplayPath(state *captureState, rel string) string {
	return filepath.Join(state.moduleRoot, filepath.FromSlash(rel))
}
