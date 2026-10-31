//go:build !unix && !windows

package secrets

// lockFile is a no-op on platforms without file locks (none are release
// targets); the in-process mutex still serialises the server's own writes.
func lockFile(string) (func(), error) { return func() {}, nil }
