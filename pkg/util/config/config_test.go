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

package config

import (
	"errors"
	"testing"
	"testing/fstest"

	. "github.com/onsi/gomega"
)

const settingsYAML = `controllerImage: quay.io/example/controller:v1
replicas: 2
`

const malformedSettingsValueYAML = `replicas:
  default: 2
`

func TestLoadDecodesYAMLFromTheSuppliedFilesystem(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	path := "settings.conf"
	fsys := fstest.MapFS{path: {Data: []byte(settingsYAML)}}

	var settings struct {
		Image    string `mapstructure:"controllerImage"`
		Replicas int    `mapstructure:"replicas"`
	}

	g.Expect(Load(fsys, path, &settings)).To(Succeed())
	g.Expect(settings.Image).To(Equal("quay.io/example/controller:v1"))
	g.Expect(settings.Replicas).To(Equal(2))
}

func TestLoadWrapsDecodeErrorsWithThePath(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)
	path := "settings.yaml"
	fsys := fstest.MapFS{path: {Data: []byte(malformedSettingsValueYAML)}}
	var settings struct {
		Replicas int `mapstructure:"replicas"`
	}

	err := Load(fsys, path, &settings)

	g.Expect(err).To(MatchError(ContainSubstring(`decode YAML config "settings.yaml"`)))
	g.Expect(errors.Unwrap(err)).To(HaveOccurred())
}
