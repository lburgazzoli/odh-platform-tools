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

// Package config loads typed YAML configuration through Viper.
package config

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"

	"github.com/spf13/viper"
)

// Load decodes a YAML file from fsys into target, which must be a pointer.
// The file extension does not affect decoding.
func Load(fsys fs.FS, path string, target any) error {
	data, err := fs.ReadFile(fsys, path)
	if err != nil {
		return fmt.Errorf("read YAML config %q: %w", path, err)
	}

	return decodeYAML(data, path, target)
}

// LoadFromPath decodes a YAML file at a native filesystem path into target,
// which must be a pointer.
func LoadFromPath(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read YAML config %q: %w", path, err)
	}

	return decodeYAML(data, path, target)
}

func decodeYAML(data []byte, path string, target any) error {
	settings := viper.New()
	settings.SetConfigType("yaml")

	if err := settings.ReadConfig(bytes.NewReader(data)); err != nil {
		return fmt.Errorf("parse YAML config %q: %w", path, err)
	}

	if err := settings.Unmarshal(target); err != nil {
		return fmt.Errorf("decode YAML config %q: %w", path, err)
	}

	return nil
}
