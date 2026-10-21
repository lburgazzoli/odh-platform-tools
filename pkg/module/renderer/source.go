package renderer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lburgazzoli/odh-platform-tools/pkg/module/config"
)

// ErrUnsafeSource identifies renderer inputs that are not safely
// contained offline: paths escaping the module root, remote or
// executable references, archive traversal, and schema references that
// could trigger fetches. Errors wrapping it are renderer execution
// failures, not definition configuration errors.
var ErrUnsafeSource = errors.New("unsafe renderer input")

// File is one captured renderer input: its module-root-relative path
// and either the validated bytes or, for a contained symlink, the
// symlink target as authored.
type File struct {
	// Path is the module-root-relative, slash-separated location of
	// the captured input inside the snapshot.
	Path string

	// Bytes holds the validated content of a regular file.
	Bytes []byte

	// LinkTarget holds the authored target of a contained symlink.
	LinkTarget string
}

// Snapshot is a private, validated copy of a module's renderer inputs,
// laid out exactly as inside the module root. Rendering must run from
// the snapshot, never from the original tree: originals cannot be
// reopened, so changes to them after capture cannot affect what is
// rendered.
type Snapshot struct {
	root       string
	moduleRoot string
	entryRel   string
	files      []File
}

// Root returns the filesystem root of the snapshot.
func (s *Snapshot) Root() string {
	return s.root
}

// Entry returns the snapshot location of the selected renderer entry:
// the chart root or the kustomization directory.
func (s *Snapshot) Entry() string {
	return filepath.Join(s.root, filepath.FromSlash(s.entryRel))
}

// Files returns the captured inputs in capture order.
func (s *Snapshot) Files() []File {
	return s.files
}

// Close removes the snapshot directory. It must be called exactly once
// on every exit path; Capture already does so when it fails.
func (s *Snapshot) Close() error {
	if s == nil || s.root == "" {
		return nil
	}

	root := s.root
	s.root = ""

	return os.RemoveAll(root)
}

// Capture captures the renderer inputs of the module described by
// document into a private snapshot, validating that every captured
// input is module-root contained and offline safe before it is used.
//
// Capture reads each input through a single open handle whose real
// location is verified to be inside the module root, so paths swapped
// for escaping symlinks between discovery and capture are rejected
// instead of copied. Policy is enforced on the captured bytes: sibling
// bases, components, resource, patch, and generator files inside the
// root are allowed, while remote, Git, or OCI sources, absolute
// references, Helm inflation, exec or external plugins, custom OpenAPI,
// unknown fields, archive traversal, missing chart dependencies, and
// non-fragment schema references fail closed with an error wrapping
// ErrUnsafeSource.
//
// The returned snapshot must be closed by the caller. Cancellation is
// returned unwrapped so it keeps its identity and precedence.
func Capture(ctx context.Context, document config.Document) (*Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	entryRel, err := relativeContainedPath(document.RendererEntry, document.ModuleRoot)
	if err != nil {
		return nil, err
	}

	root, err := os.MkdirTemp("", "odh-module-snapshot-")
	if err != nil {
		return nil, fmt.Errorf("create snapshot directory: %w", err)
	}

	snapshot := &Snapshot{
		root:       root,
		moduleRoot: document.ModuleRoot,
		entryRel:   entryRel,
	}

	state := &captureState{
		moduleRoot:    document.ModuleRoot,
		visitedDirs:   make(map[string]struct{}),
		capturedPaths: make(map[string]struct{}),
	}

	capture := captureFunc(document.Definition.Spec.Manifests.Format)
	if capture == nil {
		_ = snapshot.Close()
		return nil, fmt.Errorf("unsupported manifest format %q", document.Definition.Spec.Manifests.Format)
	}

	if err := capture(state, document.RendererEntry); err != nil {
		_ = snapshot.Close()
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		_ = snapshot.Close()
		return nil, err
	}

	snapshot.files = state.files

	if err := snapshot.materialize(); err != nil {
		_ = snapshot.Close()
		return nil, err
	}

	return snapshot, nil
}

// captureFunc returns the backend capture walk for a manifest format.
func captureFunc(format config.Format) func(*captureState, string) error {
	switch format {
	case config.FormatHelm:
		return captureHelmChart
	case config.FormatKustomize:
		return captureKustomizationDir
	default:
		return nil
	}
}

// materialize writes the captured inputs into the snapshot directory.
func (s *Snapshot) materialize() error {
	for _, file := range s.files {
		target := filepath.Join(s.root, filepath.FromSlash(file.Path))

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create snapshot directory for %s: %w", file.Path, err)
		}

		if file.LinkTarget != "" {
			if err := os.Symlink(file.LinkTarget, target); err != nil {
				return fmt.Errorf("write snapshot symlink %s: %w", file.Path, err)
			}

			continue
		}

		if err := os.WriteFile(target, file.Bytes, 0o644); err != nil {
			return fmt.Errorf("write snapshot file %s: %w", file.Path, err)
		}
	}

	return nil
}

// unsafeError reports a fail-closed source policy violation.
func unsafeError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUnsafeSource, fmt.Sprintf(format, args...))
}
