package cli

import "fmt"

// Report output formats.
const (
	FormatText = "text"
	FormatJSON = "json"
)

// outputFormat is the --output flag value. Set rejects every value
// outside the two supported formats at flag-parse time.
type outputFormat string

// String returns the current format.
func (f *outputFormat) String() string { return string(*f) }

// Set validates and stores one output format.
func (f *outputFormat) Set(value string) error {
	switch value {
	case FormatText, FormatJSON:
		*f = outputFormat(value)

		return nil
	default:
		return fmt.Errorf("output must be %q or %q", FormatText, FormatJSON)
	}
}

// Type names the flag kind for help output.
func (f *outputFormat) Type() string { return "format" }
