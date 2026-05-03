module github.com/asabla/rex

// macOS 15+/26 require Mach-O binaries to carry LC_UUID; Go toolchains
// older than 1.23 produce test binaries that the loader rejects.
// `go 1.23.0` plus GOTOOLCHAIN=auto (Go's default) makes older local
// toolchains transparently fetch a 1.23.x release that links cleanly.
go 1.23.0

require (
	github.com/spf13/cobra v1.10.2
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
)
