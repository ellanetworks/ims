# Ella IMS (beta)

**Ella IMS** lets subscribers of a private 4G or 5G network make voice calls to each other. It is an IP Multimedia Subsystem (IMS) that runs as a single application. It is designed to be easy to operate, reliable, and secure.

<p align="center">
  <img src="docs/images/ims_integration.svg" alt="Ella IMS with Ella Core"/>
</p>

> [!WARNING]
> - The API and configuration may change without notice.

## Key Features

- Voice calls
- SIM-based phone authentication with your existing HSS (IMS-AKA over Cx)
- IPsec between phones and the IMS
- Voice QoS from the 4G or 5G core (PCRF over Rx, or PCF over N5 with optional mutual TLS)
- Complete IMS core in a single binary (P-CSCF, I-CSCF, S-CSCF)
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

#### Unit and integration tests

```sh
go test ./...
```

The IPsec tests use network namespaces. They are skipped where those are unavailable, and fail instead when `CI` is set.

#### End-to-end tests

```sh
sudo modprobe -a sctp esp4 xfrm_user
cd e2e
E2E_RAT=4g ./up.sh
E2E_RAT=4g ./run.sh
```

`E2E_RAT` is `4g` or `5g`.

#### Fuzz tests

```sh
go test -run='^$' -fuzz='^FuzzParse$' ./sip
```

## Reference

### Configuration File

See [`ims.yaml`](ims.yaml).

### Compatibility

#### Core Networks

| Core      | 4G      | 5G      |
|-----------|---------|---------|
| Ella Core | ✓       | ✓       |
| Open5GS   | ✓       | ✓       |

#### Phones

| Phone              | 4G      | 5G           |
|--------------------|---------|--------------|
| Apple iPhone 11    | ✓       |              |
| Apple iPhone 16    | ✓       |              |
| Crosscall Core-Z5  | ✓       | Registration |
| Google Pixel 10a   | ✓       |              |
| Motorola Moto G 5G | ✓       |              |
| Samsung Galaxy A56 | ✓       | Registration |

## Explanation

### IPsec

Phones reach the IMS over IPsec, which the IMS sets up in the kernel. It needs `CAP_NET_ADMIN` (run as root, or add the capability to its container).

Firewalls between phones and the IMS must pass ESP (IP protocol 50).
