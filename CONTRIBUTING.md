# Contributing

## Build

### From source

```sh
npm install --prefix ui
npm run build --prefix ui
go build -o ims -ldflags "-s -w -X github.com/ellanetworks/ims/version.GitCommit=$(git rev-parse HEAD)" ./cmd/ims
```

### Container Image

```sh
rockcraft pack
```

### Snap

```sh
snapcraft pack
```

### Android app

See [`android/README.md`](android/README.md).

### Documentation

```sh
uvx --with-requirements requirements-docs.txt mkdocs build --strict
```

## Run

```sh
sudo ./ims --config ims.yaml
```

## Test

### Unit and integration tests

```sh
go test ./...
(cd sip && go test ./...)
npm test --prefix ui
```

The IPsec tests use network namespaces. They are skipped where those are unavailable, and fail instead when `CI` is set.

### End-to-end tests

```sh
sudo modprobe -a sctp esp4 xfrm_user
cd e2e
E2E_RAT=4g ./up.sh
E2E_RAT=4g ./run.sh
```

For 5G, set `E2E_RAT=5g`.

### Load tests

On Linux:

```sh
ulimit -n 200000
go test ./internal/mcptt -run '^$' -bench '^BenchmarkRegistration$' -benchtime 1000x -count 3
go test ./internal/mcptt -run '^$' -bench '^BenchmarkGroupCalls$' -benchtime 1x -count 3
```

The load tests run the MCPTT server at 1,000 registered users and 200 concurrent group calls of 100 members, in one process over loopback. They report registrations and call setups per second, and the 50th, 90th and 99th percentiles of the MCPTT access time and of the server's share of the mouth-to-ear latency (TS 22.179 §6.15.3), in milliseconds.

### Fuzz tests

```sh
cd sip
go test -run='^$' -fuzz='^FuzzParse$' .
```
