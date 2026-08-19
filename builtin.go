// Package meshmedic embeds the reviewed catalog and its lock into the binary.
//
// Without this, `go install github.com/kassvl/meshmedic/cmd/meshmedic@latest`
// produces a binary that cannot start: go install delivers an executable and
// nothing else, while the engine reads a directory of YAML files at startup.
// The very first command a new user runs died with "catalog invalid: reading
// catalog dir: no such file or directory", which is a broken promise in the
// README rather than a missing feature.
//
// Embedding also strengthens the lock's guarantee rather than weakening it.
// A released binary now carries exactly the catalog that was approved at build
// time, so the entries it runs and the hashes it checks them against travel
// together and cannot drift apart in transit. Pointing --catalog at a
// directory still overrides both, which is what a maintainer editing entries
// needs, and that path is checked against its own on-disk lock.
package meshmedic

import (
	"embed"
	"io/fs"
)

//go:embed catalog/*.yaml
var catalogFS embed.FS

//go:embed catalog.lock
var lockBytes []byte

// CatalogFS returns the embedded catalog as a filesystem rooted so that the
// entries sit at its top level, matching what a caller sees when reading a
// directory from disk.
func CatalogFS() fs.FS {
	sub, err := fs.Sub(catalogFS, "catalog")
	if err != nil {
		// Impossible: the directory is embedded at compile time, so a failure
		// here would mean the binary was built without it.
		panic("meshmedic: embedded catalog is missing: " + err.Error())
	}
	return sub
}

// Lock returns the embedded catalog.lock bytes.
func Lock() []byte { return lockBytes }
