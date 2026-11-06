---
description: Reference outlining configuration options.
---

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
    - `trusted_networks` (list of strings, optional): The networks, in CIDR notation, of the other nodes of the trust domain, such as Application Servers. The S-CSCF accepts requests only from them, the I-CSCF accepts their asserted identities, and Application Servers are sent requests only in them. Must not cover every address of a family. See [Security](../explanation/security.md#the-trust-domain).
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
- `ibcf` (object, optional): The IBCF, the border towards SIP trunks. When absent, the IBCF doesn't run, opens no ports, and calls to numbers outside the HSS get 404.
    - `port` (int, optional): The SIP port on each of `sip.addresses`, where the I-CSCF sends calls to SIP trunks. Default `5085`.
    - `external` (object): Where carriers send calls and the IBCF sends calls to carriers.
        - `addresses` (list of strings): The IP addresses, given to carriers. At least one; specific addresses, not `0.0.0.0` or `::`, and neither in `sip.addresses` nor in `sip.trusted_networks`. SIP trunks are reached from the address of their family.
        - `advertised_addresses` (list of strings, optional): The public addresses carriers send to when Ella IMS is behind a 1:1 NAT, such as a cloud VM: one for each of `addresses`, in the same order and of the same family. The IBCF puts them in the Via and Contact header fields towards carriers, and keeps binding `addresses`. The NAT must keep the ports. Default: `addresses`.
        - `port` (int, optional): The SIP port, UDP and TCP. Default `5060`. Must differ from `api.port`, `diameter.port` and `mcptt.media.control_port`, and be outside the MCPTT media ports, on the same address.
        - `tls` (object, optional): SIP over TLS towards SIP trunks, on each of `addresses`. When absent, the IBCF uses no TLS, and trunks that require it are not used.
            - `port` (int, optional): The TLS port. Default `5061`. Must differ from `port`.
            - `cert` (string, optional): The path to the PEM certificate chain of the IBCF, the leaf first, presented to carriers as server and as client. Read when the core starts.
            - `key` (string, optional): The path to the PEM private key. Set with `cert`, or neither. Without a certificate, the IBCF accepts no TLS connection: it opens and keeps open a TLS connection to each enabled trunk that uses TLS, and the carrier sends its calls over it (SIPconnect 2.0 section 17.2).
    - `keepalive` (object, optional): The OPTIONS keepalives of SIP trunks.
        - `failures` (int, optional): Keepalive rounds without an answer before a trunk is down, from `1` to `10`. Default `2`.
- `trgw` (object, optional): The TrGW, the media relay of calls over SIP trunks. Only with `ibcf`, which always relays media through it: phones and carriers only see the TrGW's addresses. Only audio is relayed; other media of a call over a trunk are rejected.
    - `phone_addresses` (list of objects, optional): The addresses the phones send media to. Default: `sip.addresses`.
        - `address` (string): The IP address of the media sockets. A specific address.
        - `advertise` (string, optional): The IP address in the SDP, of the same family. Default `address`.
    - `trunk_addresses` (list of objects, optional): The addresses carriers send media to, as `phone_addresses`. Default: `ibcf.external.addresses`, advertised as `ibcf.external.advertised_addresses`. No address can be on both lists.
    - `min_port` (int, optional): The first port of the media sockets. Even, from `1024`. Default `10000`.
    - `max_port` (int, optional): The last port. Default `19999`. Each audio stream takes two ports on a phone address and two on a trunk address. The range must not hold a SIP, API or Diameter port, `ibcf.external.port`, `mcptt.media.control_port` or MCPTT media ports on the same address.
    - `media_timeout` (duration, optional): How long an answered call may go without RTP from a side that sends and receives before the IBCF ends it, e.g. `60s`. `0` never ends calls for lack of media. Default `60s`.
- `telemetry` (object, optional): The [traces](observability.md#traces).
    - `enabled` (boolean, optional): Export traces. Default `false`.
    - `otlp_endpoint` (string): The `host:port` of an OTLP/gRPC collector, over plain gRPC. Required when `enabled` is `true`.

The SIP ports (`pcscf.port`, `pcscf.ipsec.*`, `icscf.port`, `scscf.port`, `mcptt.port`, `ibcf.port`) must all differ, must not be `5100`, which the built-in IP-SM-GW takes, and must differ from `api.port` when the API listens on a SIP address. The IPsec ports cannot be `5060` or `5061`. On a SIP address, the MCPTT media ports must not include a SIP port.

## Example

```yaml
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
ibcf:
  port: 5085
  external:
    addresses: [203.0.113.5]
    advertised_addresses: [198.51.100.5]
    port: 5060
    tls:
      port: 5061
      cert: /etc/ims/tls/ibcf.crt
      key: /etc/ims/tls/ibcf.key
  keepalive:
    failures: 2
trgw:
  min_port: 10000
  max_port: 19999
  media_timeout: 60s
diameter:
  address: 10.3.0.5
  port: 3868
telemetry:
  enabled: true
  otlp_endpoint: localhost:4317
```
