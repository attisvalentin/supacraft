//go:build !unix

package secrets

// lockFile is a no-op where flock is unavailable; the in-process mutex still
// serialises writes made by the server itself.
func lockFile(string) (func(), error) { return func() {}, nil }
