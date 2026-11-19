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

package generator

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"helm.sh/helm/v4/pkg/ignore"

	moduleerrors "github.com/lburgazzoli/odh-platform-tools/pkg/module/errors"
)

// outputStdout is the normalized Result.Output for stdout runs.
const outputStdout = "stdout"

// stagedFilePattern names publication staging files in the
// destination directory.
const stagedFilePattern = ".odh-platform-tools-generate-*.yaml"

// outputPath is the resolved destination of one generation run.
type outputPath struct {
	file bool
	path string
}

// resolveOutput treats an empty output and the literal stdout as the
// stream; every other value is a file path.
func resolveOutput(output string) (outputPath, error) {
	if output == "" || output == outputStdout {
		return outputPath{}, nil
	}

	path, err := filepath.Abs(output)
	if err != nil {
		return outputPath{}, moduleerrors.New(
			moduleerrors.ErrGenerateOutput,
			"resolve generated definition output "+output,
			moduleerrors.WithCause(err),
		)
	}

	return outputPath{file: true, path: path}, nil
}

// definitionDir is the directory the generated document resolves
// relative paths against: the destination's parent for file output,
// the working directory for stdout.
func (o outputPath) definitionDir() (string, error) {
	if !o.file {
		return os.Getwd()
	}

	return filepath.Dir(o.path), nil
}

// ensureDestinationParent fails unless the destination's parent
// directory already exists; publication never creates directories.
func ensureDestinationParent(dest string) error {
	parent := filepath.Dir(dest)

	info, err := os.Stat(parent)
	if err != nil {
		return moduleerrors.New(
			moduleerrors.ErrGenerateOutput,
			"generated definition destination parent "+parent+" is not readable",
			moduleerrors.WithCause(err),
		)
	}

	if !info.IsDir() {
		return moduleerrors.New(
			moduleerrors.ErrGenerateOutput,
			"generated definition destination parent "+parent+" is not a directory",
		)
	}

	return nil
}

// guardChartDestination rejects a file destination the pinned Helm
// chart loader may consume, unless the chart's own .helmignore rules
// exclude it. Both the lexical and the physical (symlink-resolved)
// containment forms are checked; when alias resolution fails, the
// destination is rejected as indeterminate rather than assumed safe.
func guardChartDestination(chartRoot string, dest string) error {
	chartPhysical, err := filepath.EvalSymlinks(chartRoot)
	if err != nil {
		return indeterminateDestination(chartRoot, err)
	}

	destDir := filepath.Dir(dest)
	destPhysical, err := filepath.EvalSymlinks(destDir)
	if err != nil {
		return indeterminateDestination(destDir, err)
	}

	rules, err := chartIgnoreRules(chartPhysical)
	if err != nil {
		return err
	}

	base := filepath.Base(dest)
	forms := [][2]string{
		{chartRoot, destDir},
		{chartPhysical, destPhysical},
	}

	for _, form := range forms {
		if !withinBase(form[0], form[1]) {
			continue
		}

		excluded, err := rulesExclude(rules, form[0], form[1], base)
		if err != nil {
			return indeterminateDestination(destDir, err)
		}

		if !excluded {
			return consumedDestination(dest, chartRoot)
		}
	}

	return guardChartSymlinks(rules, chartRoot, chartPhysical, dest, destPhysical, base)
}

// consumedDestination rejects a destination the chart may load.
func consumedDestination(dest string, chartRoot string) error {
	return moduleerrors.New(
		moduleerrors.ErrInvalidGenerateOptions,
		"output "+dest+" may be consumed by the chart at "+chartRoot+
			"; move it outside the chart or exclude it in .helmignore",
	)
}

// chartWalkBudget bounds the symlink-aware chart walk; charts whose
// chart-relative paths proliferate beyond it are indeterminate.
const chartWalkBudget = 4096

// errChartWalkBudget reports an oversized chart walk.
var errChartWalkBudget = errors.New("chart walk exceeded its entry budget")

// visibleDir pairs one chart-relative directory path with the
// physical directory the pinned loader reaches there.
type visibleDir struct {
	// rel is the slash-separated chart-relative path; "" is the root.
	rel string

	// dir is the physical directory.
	dir string
}

// guardChartSymlinks rejects destinations the pinned Helm chart
// loader reaches through chart-internal symlinks: the loader follows
// directory symlinks, so files under their targets are chart content
// under the symlink's chart-relative path even when the targets lie
// outside the chart root. Walked entries are evaluated against the
// chart's ignore rules with the resolved target's file info,
// matching the loader's resolve-then-ignore order; resolution
// failures and oversized walks are indeterminate.
func guardChartSymlinks(
	rules *ignore.Rules,
	chartRoot string,
	chartPhysical string,
	dest string,
	destPhysical string,
	base string,
) error {
	seen := map[string]bool{"": true}
	queue := []visibleDir{{"", chartPhysical}}

	for walked := 1; len(queue) > 0; walked++ {
		if walked > chartWalkBudget {
			return indeterminateDestination(chartRoot, errChartWalkBudget)
		}

		current := queue[0]
		queue = queue[1:]

		// The root pair duplicates the physical containment form.
		if current.rel != "" && withinBase(current.dir, destPhysical) {
			excluded, err := rulesExcludeUnder(rules, current.rel, current.dir, destPhysical, base)
			if err != nil {
				return indeterminateDestination(dest, err)
			}

			if !excluded {
				return consumedDestination(dest, chartRoot)
			}
		}

		entries, err := os.ReadDir(current.dir)
		if err != nil {
			return indeterminateDestination(current.dir, err)
		}

		for _, entry := range entries {
			rel := path.Join(current.rel, entry.Name())
			child := filepath.Join(current.dir, entry.Name())

			info, err := entry.Info()
			if err != nil {
				return indeterminateDestination(child, err)
			}

			if info.Mode()&os.ModeSymlink == 0 {
				if !info.IsDir() {
					continue
				}

				// Directory entries are ignore-checked with their
				// own info before descending, like the loader.
				if rules.Ignore(rel, info) {
					continue
				}

				if !seen[rel] {
					seen[rel] = true
					queue = append(queue, visibleDir{rel: rel, dir: child})
				}

				continue
			}

			// Symlinks resolve first: the loader follows them before
			// ignore rules are applied.
			resolved, err := filepath.EvalSymlinks(child)
			if err != nil {
				return indeterminateDestination(child, err)
			}

			target, err := os.Lstat(resolved)
			if err != nil {
				return indeterminateDestination(resolved, err)
			}

			if rules.Ignore(rel, target) {
				continue
			}

			if target.IsDir() {
				if !seen[rel] {
					seen[rel] = true
					queue = append(queue, visibleDir{rel: rel, dir: resolved})
				}

				continue
			}

			// A file symlink can name the destination itself.
			if resolved == filepath.Join(destPhysical, base) {
				return consumedDestination(dest, chartRoot)
			}
		}
	}

	return nil
}

// chartIgnoreRules loads the chart's .helmignore with the pinned Helm
// parser and the loader's default rules. A malformed ignore file is a
// definition failure, matching the chart loader's behavior.
func chartIgnoreRules(chartRoot string) (*ignore.Rules, error) {
	rules := ignore.Empty()

	ignoreFile := filepath.Join(chartRoot, ignore.HelmIgnore)
	if _, err := os.Stat(ignoreFile); err == nil {
		parsed, err := ignore.ParseFile(ignoreFile)
		if err != nil {
			return nil, moduleerrors.New(
				moduleerrors.ErrInvalidDefinition,
				"parse chart ignore file "+ignoreFile,
				moduleerrors.WithCause(err),
			)
		}

		rules = parsed
	}

	rules.AddDefaults()

	return rules, nil
}

// rulesExclude reports whether the pinned loader's directory walk
// would skip the destination: an ignored ancestor directory skips its
// whole subtree, and the file rules are evaluated against the
// destination's slash-normalized relative path.
func rulesExclude(
	rules *ignore.Rules,
	root string,
	destDir string,
	base string,
) (bool, error) {
	return rulesExcludeUnder(rules, "", root, destDir, base)
}

// rulesExcludeUnder evaluates the destination's ignore status under
// one chart-visible directory, composing the chart-relative prefix
// with the physical relative path for rule evaluation. Symlinked
// subtrees carry the prefix of their chart-relative alias.
func rulesExcludeUnder(
	rules *ignore.Rules,
	prefix string,
	root string,
	destDir string,
	base string,
) (bool, error) {
	relative, err := filepath.Rel(root, destDir)
	if err != nil {
		return false, err
	}

	compose := func(rel string) string {
		if prefix == "" {
			return rel
		}

		return prefix + "/" + rel
	}

	ancestor := ""
	for _, segment := range strings.Split(filepath.ToSlash(relative), "/") {
		if segment == "." {
			continue
		}

		ancestor = path.Join(ancestor, segment)

		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(ancestor)))
		if err != nil {
			return false, err
		}

		if rules.Ignore(compose(ancestor), info) {
			return true, nil
		}
	}

	return rules.Ignore(compose(path.Join(filepath.ToSlash(relative), base)),
		regularFileInfo{name: base}), nil
}

// withinBase reports whether dir is root or lies under it.
func withinBase(root string, dir string) bool {
	relative, err := filepath.Rel(root, dir)
	if err != nil {
		return false
	}

	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// indeterminateDestination reports a destination whose chart
// containment cannot be determined safely.
func indeterminateDestination(path string, cause error) error {
	return moduleerrors.New(
		moduleerrors.ErrInvalidGenerateOptions,
		"cannot safely determine whether the destination near "+path+" is inside the chart",
		moduleerrors.WithCause(cause),
	)
}

// publishFile stages complete bytes next to dest and installs them
// with a no-replace hard link; the link is the file commit point. A
// cancellation observed before staging or at the precommit
// checkpoint between staging and installation prevents publication,
// and a successful installation is final.
func publishFile(ctx context.Context, dest string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	staged, err := stageFile(dest, data)
	if err != nil {
		return err
	}

	committed := false

	defer func() {
		if !committed {
			_ = os.Remove(staged)
		}
	}()

	// The precommit checkpoint: cancellation observed after staging
	// still prevents publication; a cancellation racing with the
	// installation may lose to the commit.
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := os.Link(staged, dest); err != nil {
		return outputFailure("install generated definition at "+dest, err)
	}

	committed = true
	_ = os.Remove(staged)

	return nil
}

// stageFile writes complete bytes to a closed staging file next to
// dest. Ownership of the file transfers to the caller only on
// success; a failed staging attempt removes its own file, so no
// partial plaintext overrides survive a disk-full or close failure.
func stageFile(dest string, data []byte) (staged string, failure error) {
	temp, err := os.CreateTemp(filepath.Dir(dest), stagedFilePattern)
	if err != nil {
		return "", outputFailure("stage generated definition next to "+dest, err)
	}

	// created stays owned by this function until the successful
	// return transfers it; the named return cannot hold it, because
	// error returns clear it before deferred cleanup runs.
	created := temp.Name()

	defer func() {
		if failure != nil {
			_ = os.Remove(created)
		}
	}()

	if _, err := temp.Write(data); err != nil {
		// Best effort: closing after a failed write is safe to
		// ignore once the write failure is the reported error and
		// the deferred cleanup removes the partial file.
		_ = temp.Close()

		return "", outputFailure("write staged generated definition "+created, err)
	}

	if err := temp.Close(); err != nil {
		return "", outputFailure("close staged generated definition "+created, err)
	}

	return created, nil
}

// outputFailure classifies a staging or publication failure.
func outputFailure(stage string, cause error) error {
	return moduleerrors.New(
		moduleerrors.ErrGenerateOutput,
		stage+": "+cause.Error(),
		moduleerrors.WithCause(cause),
	)
}

// regularFileInfo describes the not-yet-created destination file for
// ignore-rule evaluation.
type regularFileInfo struct {
	name string
}

func (info regularFileInfo) Name() string       { return info.name }
func (info regularFileInfo) Size() int64        { return 0 }
func (info regularFileInfo) Mode() os.FileMode  { return 0o600 }
func (info regularFileInfo) ModTime() time.Time { return time.Time{} }
func (info regularFileInfo) IsDir() bool        { return false }
func (info regularFileInfo) Sys() any           { return nil }
