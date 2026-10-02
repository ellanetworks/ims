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

The IMS installs IPsec security associations for phones, so it needs
`CAP_NET_ADMIN` and the `xfrm_user`, `esp4`, `esp6` and `authenc` kernel
modules. It refuses to start without them. Firewalls between phones and the
IMS must pass ESP (IP protocol 50).

### Test

```sh
go test ./...
```

The IPsec tests run real ESP between network namespaces in an unprivileged
user namespace. They are skipped where user namespaces are unavailable, and
fail instead when `CI` is set.

## Reference

### Configuration File

See [`ims.yaml`](ims.yaml).
