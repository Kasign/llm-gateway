package buildinfo

// Revision and Modified are injected by release builds; empty values fall back to Go VCS build metadata.
var (
	Revision = ""
	Modified = ""
)
