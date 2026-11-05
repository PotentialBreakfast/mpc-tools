module mpctools

go 1.24.7

replace golang.org/x/crypto => github.com/golang/crypto v0.40.0

replace golang.org/x/sys => github.com/golang/sys v0.34.0

replace golang.org/x/term => github.com/golang/term v0.33.0

replace golang.org/x/text => github.com/golang/text v0.27.0

replace golang.org/x/net => github.com/golang/net v0.42.0

replace gopkg.in/yaml.v3 => github.com/go-yaml/yaml/v3 v3.0.1

replace golang.org/x/tools => github.com/golang/tools v0.34.0

replace golang.org/x/mod => github.com/golang/mod v0.25.0

replace golang.org/x/sync => github.com/golang/sync v0.15.0

replace gopkg.in/check.v1 => github.com/go-check/check v0.0.0-20161208181325-20d25e280405

replace golang.org/x/telemetry => github.com/golang/telemetry v0.0.0-20240521205824-bda55230c457

require (
	github.com/pkg/sftp v1.13.9
	golang.org/x/crypto v0.40.0
)

require (
	github.com/kr/fs v0.1.0 // indirect
	golang.org/x/sys v0.34.0 // indirect
)
