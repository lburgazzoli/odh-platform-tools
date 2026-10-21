package renderer

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// captureState carries the incremental state of one capture run: the
// canonical module root that every input must stay inside, the captured
// files, and the bookkeeping that keeps walks finite and idempotent.
type captureState struct {
	moduleRoot    string
	files         []File
	visitedDirs   map[string]struct{}
	capturedPaths map[string]struct{}
}

// add records a captured file unless its location was captured already.
func (s *captureState) add(file File) {
	if _, dup := s.capturedPaths[file.Path]; dup {
		return
	}

	s.capturedPaths[file.Path] = struct{}{}
	s.files = append(s.files, file)
}

// fileAt returns the captured file at a module-root-relative path.
func (s *captureState) fileAt(rel string) (File, bool) {
	for _, file := range s.files {
		if file.Path == rel {
			return file, true
		}
	}

	return File{}, false
}

// filesUnder returns the captured files at or below a module-root-relative
// directory.
func (s *captureState) filesUnder(rel string) []File {
	prefix := rel + "/"

	var files []File
	for _, file := range s.files {
		if file.Path == rel || strings.HasPrefix(file.Path, prefix) {
			files = append(files, file)
		}
	}

	return files
}

// relativeContainedPath returns the module-root-relative form of path,
// failing when path is not strictly inside root. Containment is
// component-aware: a root prefix that only shares a name (mod versus
// mod-evil) does not pass.
func relativeContainedPath(path, root string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", fmt.Errorf("relate %s to module root %s: %w", path, root, err)
	}

	if rel == "." || rel == ".." || filepath.IsAbs(rel) ||
		rel == ".."+string(filepath.Separator) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", unsafeError("path %s escapes module root %s", path, root)
	}

	return rel, nil
}

// captureRegular reads the file at path into the snapshot through a
// single open handle whose real location is verified to be inside the
// module root. A path swapped for an escaping symlink between discovery
// and capture therefore cannot smuggle outside content in: the handle
// is the thing that is read, and the handle is the thing that is
// verified.
func captureRegular(state *captureState, path string) (File, error) {
	file, err := os.Open(path) //nolint:gosec // path is validated below via the handle
	if err != nil {
		if os.IsNotExist(err) {
			return File{}, unsafeError("missing input %s", path)
		}

		return File{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close() //nolint:errcheck // read-only handle

	// The handle resolves to the file the kernel actually opened, with
	// every symlink component already followed.
	resolvedPath, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(int(file.Fd())))
	if err != nil {
		return File{}, fmt.Errorf("resolve handle for %s: %w", path, err)
	}

	resolvedPath = strings.TrimSuffix(resolvedPath, " (deleted)")

	rel, err := relativeContainedPath(resolvedPath, state.moduleRoot)
	if err != nil {
		return File{}, unsafeError("input %s resolves outside module root %s", path, state.moduleRoot)
	}

	info, err := file.Stat()
	if err != nil {
		return File{}, fmt.Errorf("stat %s: %w", path, err)
	}

	if !info.Mode().IsRegular() {
		return File{}, unsafeError("input %s is not a regular file", path)
	}

	data, err := io.ReadAll(file)
	if err != nil {
		return File{}, fmt.Errorf("read %s: %w", path, err)
	}

	if existing, ok := state.fileAt(filepath.ToSlash(rel)); ok {
		return existing, nil
	}

	captured := File{Path: filepath.ToSlash(rel), Bytes: data}
	state.add(captured)

	return captured, nil
}

// captureLink records a contained symlink, preserving its authored
// relative target so references through the link keep working inside
// the snapshot. Absolute targets and targets resolving outside the
// module root are rejected.
func captureLink(state *captureState, linkPath string) error {
	target, err := os.Readlink(linkPath)
	if err != nil {
		return fmt.Errorf("read symlink %s: %w", linkPath, err)
	}

	if filepath.IsAbs(target) {
		return unsafeError("symlink %s has absolute target %q", linkPath, target)
	}

	resolved, err := filepath.EvalSymlinks(linkPath)
	if err != nil {
		return unsafeError("broken symlink %s", linkPath)
	}

	if _, err := relativeContainedPath(resolved, state.moduleRoot); err != nil {
		return unsafeError("symlink %s resolves outside module root %s", linkPath, state.moduleRoot)
	}

	rel, err := relativeContainedPath(linkPath, state.moduleRoot)
	if err != nil {
		return err
	}

	state.add(File{Path: filepath.ToSlash(rel), LinkTarget: target})

	return nil
}

// captureSymlinkedFile captures a file reached through a symlink: the
// target's bytes at its canonical location, plus the symlink itself so
// references by the link's path keep resolving in the snapshot.
func captureSymlinkedFile(state *captureState, linkPath string) error {
	resolved, err := filepath.EvalSymlinks(linkPath)
	if err != nil {
		return unsafeError("broken symlink %s", linkPath)
	}

	if _, err := captureRegular(state, resolved); err != nil {
		return err
	}

	return captureLink(state, linkPath)
}

// captureReferenceFile captures an input that must be a regular file,
// reached either directly or through a contained symlink. Directory and
// special-file references are rejected.
func captureReferenceFile(state *captureState, path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return unsafeError("missing input %s", path)
		}

		return fmt.Errorf("stat %s: %w", path, err)
	}

	switch {
	case info.Mode().IsRegular():
		_, err := captureRegular(state, path)
		return err
	case info.Mode()&os.ModeSymlink != 0:
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return unsafeError("broken symlink %s", path)
		}

		resolvedInfo, err := os.Stat(resolved)
		if err != nil {
			return fmt.Errorf("stat %s: %w", resolved, err)
		}

		if resolvedInfo.IsDir() {
			return unsafeError("reference %s is a directory, not a file", path)
		}

		return captureSymlinkedFile(state, path)
	case info.IsDir():
		return unsafeError("reference %s is a directory, not a file", path)
	default:
		return unsafeError("reference %s is not a regular file", path)
	}
}

// walkTree captures every file below dir, skipping repository metadata
// directories. Symlinked files and directories are followed only when
// they resolve inside the module root; the per-file handle check in
// captureRegular enforces the same rule even if the tree changes while
// walking.
func walkTree(state *captureState, dir string) error {
	if _, seen := state.visitedDirs[dir]; seen {
		return nil
	}
	state.visitedDirs[dir] = struct{}{}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read directory %s: %w", dir, err)
	}

	for _, entry := range entries {
		entryPath := filepath.Join(dir, entry.Name())

		switch {
		case entry.IsDir():
			if entry.Name() == ".git" || entry.Name() == ".context" {
				continue
			}

			if err := walkTree(state, entryPath); err != nil {
				return err
			}
		case entry.Type()&fs.ModeSymlink != 0:
			if err := walkTreeSymlink(state, entryPath); err != nil {
				return err
			}
		case entry.Type().IsRegular():
			if _, err := captureRegular(state, entryPath); err != nil {
				return err
			}
		default:
			return unsafeError("input %s is not a regular file", entryPath)
		}
	}

	return nil
}

// walkTreeSymlink captures a symlinked tree entry: directories resolve
// and recurse (with the link recorded), files capture through the link.
func walkTreeSymlink(state *captureState, linkPath string) error {
	resolved, err := filepath.EvalSymlinks(linkPath)
	if err != nil {
		return unsafeError("broken symlink %s", linkPath)
	}

	if _, err := relativeContainedPath(resolved, state.moduleRoot); err != nil {
		return unsafeError("symlink %s resolves outside module root %s", linkPath, state.moduleRoot)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("stat %s: %w", resolved, err)
	}

	if info.IsDir() {
		if err := captureLink(state, linkPath); err != nil {
			return err
		}

		return walkTree(state, resolved)
	}

	return captureSymlinkedFile(state, linkPath)
}

// unsafeReferenceReason reports why a renderer reference string cannot
// be a contained local input, if it cannot.
func unsafeReferenceReason(ref string) (string, bool) {
	switch {
	case ref == "":
		return "empty reference", true
	case strings.HasPrefix(ref, "/"):
		return "absolute reference", true
	case strings.Contains(ref, "://"):
		return "remote URL", true
	case strings.HasPrefix(ref, "git@"), strings.HasPrefix(ref, "git::"), strings.HasPrefix(ref, "oci::"):
		return "remote source", true
	case strings.HasPrefix(ref, "github.com/"), strings.HasPrefix(ref, "gitlab.com/"):
		return "remote Git source", true
	case strings.Contains(ref, "::"):
		return "remote source", true
	default:
		return "", false
	}
}

// decodeYAMLDocument decodes exactly one YAML document into generic
// values, rejecting empty input, extra documents, non-object documents,
// and duplicate keys.
func decodeYAMLDocument(data []byte) (map[string]any, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))

	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode YAML document: %w", err)
	}

	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("decode YAML document: multiple documents")
		}

		return nil, fmt.Errorf("decode YAML document: %w", err)
	}

	mapping, ok := document.(map[string]any)
	if !ok {
		return nil, errors.New("decode YAML document: document is not a mapping")
	}

	return mapping, nil
}
