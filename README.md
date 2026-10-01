# Ella IMS (alpha)

IP Multimedia Subsystem for private cellular networks.

> [!WARNING]
> - This project is under early development.
> - The API and configuration may change without notice.

## Key Features

- HTTP API

## How-to Guides

### Build

#### From source

```sh
go build -o ims ./cmd/ims
```

#### Container Image

```sh
rockcraft pack
```

### Run

```sh
./ims --config ims.yaml
```

### Test

```sh
go test ./...
```

## Reference

### Configuration File

See [`ims.yaml`](ims.yaml).
