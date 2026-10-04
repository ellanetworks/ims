# Ella IMS (alpha)

<p align="center">
  <img src="docs/images/ims_integration.svg" alt="Ella IMS with Ella Core"/>
</p>

IP Multimedia Subsystem for private cellular networks.

> [!WARNING]
> - This project is under early development.
> - The API and configuration may change without notice.

## Key Features

- Complete IMS core in a single binary (P-CSCF, I-CSCF, S-CSCF)
- SIM-based phone authentication with your existing HSS (IMS-AKA over Cx;  works with Ella Core)
- IPsec between phones and the IMS
- Embedded database (SQLite)
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
sudo ./ims --config ims.yaml
```

### Test

```sh
go test ./...
```

The IPsec tests use network namespaces. They are skipped where those are
unavailable, and fail instead when `CI` is set.

## Reference

### Configuration File

See [`ims.yaml`](ims.yaml).

## Explanation

### IPsec

Phones reach the IMS over IPsec, which the IMS sets up in the kernel. It needs
`CAP_NET_ADMIN` (run as root, or add the capability to its container).

Firewalls between phones and the IMS must pass ESP (IP protocol 50).
