// Package paths centralizes home-directory lookup so callers can sandbox
// skillvendor by setting SKILLVENDOR_HOME (e.g., for integration tests or to
// run two installations side-by-side).
package paths

import "os"

// Home returns SKILLVENDOR_HOME if set, otherwise the user's real home dir.
func Home() (string, error) {
	if h := os.Getenv("SKILLVENDOR_HOME"); h != "" {
		return h, nil
	}
	return os.UserHomeDir()
}
