module github.com/ellanetworks/ims

go 1.26.5

require (
	github.com/ellanetworks/core/diameter v0.0.0-20261009192142-6cec7a535040
	github.com/ellanetworks/core/sctp v0.0.0-20260928204907-fc197c42dc3f
	github.com/ellanetworks/ims/sip v0.0.0-00010101000000-000000000000
	github.com/google/uuid v1.6.0
	github.com/mattn/go-sqlite3 v1.14.52
	github.com/prometheus/client_golang v1.25.0
	github.com/prometheus/client_model v0.6.3
	github.com/prometheus/common v0.72.1
	golang.org/x/sys v0.49.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/procfs v0.22.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace github.com/ellanetworks/ims/sip => ./sip
