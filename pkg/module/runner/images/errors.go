package images

// ImageSourceError reports a related-images input failure: the
// selected file is unreadable or malformed. Its Error text names the
// file but never its content, so dotenv values are not echoed in
// command output; the original cause stays reachable through Unwrap
// for callers that need the details.
type ImageSourceError struct {
	path  string
	cause error
}

// Error implements error with safe, content-free text.
func (e *ImageSourceError) Error() string {
	return "invalid related images file " + e.path
}

// Unwrap exposes the underlying cause, which may quote file content.
func (e *ImageSourceError) Unwrap() error {
	return e.cause
}
