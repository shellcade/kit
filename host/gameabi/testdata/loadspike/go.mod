module loadspike

go 1.26.3

// kit v2.7.0 ships the large-room callbacks + lifecycle declarations this
// guest exercises. The committed .wasm artifacts
// are what tests/benchmarks load; the module exists to rebuild them (same
// pattern as testdata/fixture).
require github.com/shellcade/kit/v2 v2.7.0

require (
	github.com/extism/go-pdk v1.1.3 // indirect
	golang.org/x/sys v0.44.0 // indirect
	golang.org/x/term v0.43.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
