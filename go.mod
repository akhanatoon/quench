module github.com/akhanatoon/quench

go 1.23.0

retract (
	[v0.2.0, v0.5.3] // Premature pre-hardening tags; superseded by v0.1.1+.
)

require (
	github.com/spf13/cobra v1.8.1
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
)
