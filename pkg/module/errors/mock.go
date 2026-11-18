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

package errors

import stderrors "errors"

// ErrInvalidMockAnnotation identifies malformed mock condition controls.
var ErrInvalidMockAnnotation = stderrors.New("invalid mock condition annotation")

// ErrInvalidMockOptions identifies invalid settings for module mock.
var ErrInvalidMockOptions = stderrors.New("invalid mock options")

// ErrMockRuntime identifies a failure starting or running the mock controller.
var ErrMockRuntime = stderrors.New("mock controller failed")

// ErrInvalidMockPlatformConfiguration identifies a missing or invalid platform version input.
var ErrInvalidMockPlatformConfiguration = stderrors.New("invalid mock platform configuration")
