// Package engines owns where the proxy engines and their geo data live: finding
// them, and — on the platform that ships them inside the binary — writing them
// out.
//
// It deliberately does not embed anything. //go:embed cannot reference a path
// outside the directory of the file that declares it, and the data sits in bin/
// at the repository root; so the directives stay beside the data in package
// main, and the bytes are handed here as File values. That split is what lets
// this logic live somewhere other than the root package while the payload stays
// where the build system expects it.
package engines

// File is one thing to write into the engine directory.
type File struct {
	Name string
	Data []byte
	Perm uint32
	// KeepExisting marks a file that must never overwrite one already on disk.
	// The rule-set baselines are refreshed from upstream at runtime, and
	// re-writing the shipped copy at every launch would silently undo that.
	KeepExisting bool
}
