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

package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// debugEnabled reports whether the root --debug flag was set on the
// executed command line. Persistent flag values are shared with every
// subcommand through the same flag objects, so the root's flag set
// reflects whatever the executed subcommand parsed.
func debugEnabled(command *cobra.Command) bool {
	// Registration-time invariant: NewRootCommand defines the flag
	// before any execution, so lookup cannot fail.
	debug, err := command.PersistentFlags().GetBool("debug")
	if err != nil {
		return false
	}

	return debug
}

// writeErrorChain writes the error and every wrapped cause, one line
// per cause, descending into each branch of joined errors. Classified
// commands summarize their failures with typed fields only; this is the
// opt-in diagnostic view that surfaces the raw chain.
func writeErrorChain(w io.Writer, err error) error {
	if err == nil {
		return nil
	}

	var builder strings.Builder

	fmt.Fprintf(&builder, "module debug: %s\n", err)

	var writeCauses func(err error, depth int)

	writeCauses = func(err error, depth int) {
		indent := strings.Repeat("  ", depth)

		if multi, ok := err.(interface{ Unwrap() []error }); ok {
			for _, branch := range multi.Unwrap() {
				if branch == nil {
					continue
				}

				fmt.Fprintf(&builder, "module debug: %scause: %s\n", indent, branch)
				writeCauses(branch, depth+1)
			}

			return
		}

		if single, ok := err.(interface{ Unwrap() error }); ok {
			if cause := single.Unwrap(); cause != nil {
				fmt.Fprintf(&builder, "module debug: %scause: %s\n", indent, cause)
				writeCauses(cause, depth+1)
			}
		}
	}

	writeCauses(err, 1)

	if _, err := io.WriteString(w, builder.String()); err != nil {
		return fmt.Errorf("writing the error chain failed: %w", err)
	}

	return nil
}
