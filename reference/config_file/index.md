# Configuration File

Ella IMS is configured using a YAML file. Start Ella IMS with the `--config` flag to specify its path. Unknown keys are rejected.

## Parameters

- `logging` (object, optional): The logging configuration.
  - `level` (string, optional): The log level: `debug`, `info`, `warn`, or `error`. Default `info`.
- `db` (object): The database configuration.
  - `path` (string): The path to the SQLite database file.
- `api` (object): The HTTP API and UI.
  - `address` (string): The IP address to listen on. `0.0.0.0` or `::` for all IPv4 and IPv6 addresses.
  - `port` (int, optional): The TCP port to listen on. Default `5020`.
  - `tls` (object, optional): Serve over HTTPS. Default: plain HTTP.
    - `cert` (string): The path to the PEM certificate chain, the server's certificate first.
    - `key` (string): The path to the PEM private key.
- `sip` (object): SIP, for every CSCF.
  - `addresses` (list of strings): The IP addresses the CSCFs listen on and phones reach. At least one; specific addresses, not `0.0.0.0` or `::`.
  - `trusted_networks` (list of strings, optional): The networks, in CIDR notation, of the other nodes of the trust domain, such as Application Servers. The S-CSCF accepts requests only from them, the I-CSCF accepts their asserted identities, and Application Servers are sent requests only in them. Must not cover every address of a family. See [Security](https://docs.ellanetworks.com/ims/explanation/security/#the-trust-domain).
- `pcscf` (object, optional): The P-CSCF.
  - `port` (int, optional): The SIP port, UDP and TCP. Default `5060`.
  - `ipsec` (object, optional): The IPsec ports.
    - `server_port` (int, optional): The protected server port. Default `5063`.
    - `client_ports` (list of 2 ints, optional): The protected client ports. Default `[5064, 5065]`.
- `icscf` (object, optional): The I-CSCF.
  - `port` (int, optional): The SIP port, UDP and TCP. Default `5070`.
- `scscf` (object, optional): The S-CSCF.
  - `port` (int, optional): The SIP port, UDP and TCP. Default `5080`.
  - `as_timeout` (duration, optional): How long an Application Server has to respond before the default handling of its iFC applies, e.g. `4s`. Under `32s`. Default `4s`.
- `diameter` (object): Diameter, to the HSS and the PCRF.
  - `address` (string): The IP address of the Diameter connections, advertised to peers. A specific address, not `0.0.0.0` or `::`.
  - `port` (int, optional): The port. Default `3868`.
- `n5` (object, optional): N5, to a PCF. Required to select the PCF as the policy function.
  - `notify` (object): Where the P-CSCF receives the PCF's notifications.
    - `address` (string): The IP address. A specific address, not `0.0.0.0` or `::`.
    - `port` (int, optional): The TCP port. `0`, the default, lets the system choose one. Must differ from the other TCP ports on its address.
  - `tls` (object, optional): Mutual TLS on N5. Required for a PCF URI with the `https` scheme. The files are read when the PCF URI is set.
    - `ca` (string): The path to the PEM file of the CA certificates that the PCF's certificate must chain to.
    - `cert` (string): The path to the PEM file of Ella IMS's certificate, presented as both client and server. Must be valid for `n5.notify.address`.
    - `key` (string): The path to the PEM file of the certificate's private key.
- `mcptt` (object, optional): The built-in MCPTT server. When absent, the MCPTT server doesn't run and opens no ports.
  - `mode` (string, optional): `ims`, the default, or `direct`: the MCPTT server is the registrar of its clients, without the IMS, for labs and tests.
  - `port` (int, optional): The SIP port on each of `sip.addresses`. In `direct` mode, the UDP and TCP port clients send to. Default `5090`.
  - `media` (object): The media relay.
    - `address` (string): The IP address of the media sockets.
    - `advertise` (string, optional): The IP address in the SDP, of the same family as `address`. Required if `address` is `0.0.0.0` or `::`. Default `address`.
    - `min_port` (int, optional): The first port of the voice legs. Even. Default `20000`.
    - `max_port` (int, optional): The last port of the voice legs. Default `20999`.
    - `control_port` (int, optional): The port of the floor control channels, outside the voice legs' range. Default `21000`.

The SIP ports (`pcscf.port`, `pcscf.ipsec.*`, `icscf.port`, `scscf.port`, `mcptt.port`) must all differ, must not be `5100`, which the built-in IP-SM-GW takes, and must differ from `api.port` when the API listens on a SIP address. The IPsec ports cannot be `5060` or `5061`. On a SIP address, the MCPTT media ports must not include a SIP port.

## Example

```
logging:
  level: info
db:
  path: ims.db
api:
  address: 0.0.0.0
  port: 5020
sip:
  addresses: [10.6.0.5]
pcscf:
  port: 5060
  ipsec:
    server_port: 5063
    client_ports: [5064, 5065]
icscf:
  port: 5070
scscf:
  port: 5080
diameter:
  address: 10.3.0.5
  port: 3868
```
