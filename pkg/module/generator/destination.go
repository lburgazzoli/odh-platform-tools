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
	"os"
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
			return moduleerrors.New(
				moduleerrors.ErrInvalidGenerateOptions,
				"output "+dest+" may be consumed by the chart at "+chartRoot+
					"; move it outside the chart or exclude it in .helmignore",
			)
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
	relative, err := filepath.Rel(root, destDir)
	if err != nil {
		return false, err
	}

	ancestor := ""
	for _, segment := range strings.Split(relative, string(filepath.Separator)) {
		if segment == "." {
			continue
		}

		ancestor = filepath.Join(ancestor, segment)

		info, err := os.Stat(filepath.Join(root, ancestor))
		if err != nil {
			return false, err
		}

		if rules.Ignore(filepath.ToSlash(ancestor), info) {
			return true, nil
		}
	}

	return rules.Ignore(filepath.ToSlash(filepath.Join(relative, base)), regularFileInfo{name: base}), nil
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
// cancellation observed before staging prevents publication, and a
// successful installation is final.
func publishFile(ctx context.Context, dest string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	temp, err := os.CreateTemp(filepath.Dir(dest), stagedFilePattern)
	if err != nil {
		return outputFailure("stage generated definition next to "+dest, err)
	}

	staged := temp.Name()
	committed := false

	defer func() {
		if !committed {
			_ = os.Remove(staged)
		}
	}()

	if _, err := temp.Write(data); err != nil {
		// Best effort: the staging file is removed by the deferred
		// cleanup and the write failure is the reported error.
		_ = temp.Close()

		return outputFailure("write staged generated definition "+staged, err)
	}

	if err := temp.Close(); err != nil {
		return outputFailure("close staged generated definition "+staged, err)
	}

	if err := os.Link(staged, dest); err != nil {
		return outputFailure("install generated definition at "+dest, err)
	}

	committed = true
	_ = os.Remove(staged)

	return nil
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
