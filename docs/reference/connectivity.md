---
description: Reference of the network interfaces and ports of Ella IMS.
---

# Connectivity

| Interface | Peer | Transport | Address | Port |
| --------- | ---- | --------- | ------- | ---- |
| API | Operators, Prometheus | TCP | `api.address` | `api.port` (`5020`) |
| SIP: P-CSCF | Phones | UDP and TCP | `sip.addresses` | `pcscf.port` (`5060`) |
| SIP: P-CSCF, IPsec | Phones | UDP and TCP, in ESP | `sip.addresses` | `pcscf.ipsec.server_port` (`5063`), `pcscf.ipsec.client_ports` (`5064`, `5065`) |
| SIP: I-CSCF | Application Servers, other networks | UDP and TCP | `sip.addresses` | `icscf.port` (`5070`) |
| SIP: S-CSCF | Application Servers | UDP and TCP | `sip.addresses` | `scscf.port` (`5080`) |
| SIP: IBCF, internal | I-CSCF | UDP and TCP | `sip.addresses` | `ibcf.port` (`5085`), while `ibcf` is set |
| SIP: IBCF, towards SIP trunks | Carriers | UDP and TCP | `ibcf.external.addresses`, reached at `ibcf.external.advertised_addresses` behind a 1:1 NAT | `ibcf.external.port` (`5060`), while `ibcf` is set |
| SIP: IBCF, towards SIP trunks over TLS | Carriers | TLS over TCP | `ibcf.external.addresses`, reached at `ibcf.external.advertised_addresses` behind a 1:1 NAT | `ibcf.external.tls.port` (`5061`), while `ibcf.external.tls` is set |
| TrGW media, phones | Phones | UDP | `trgw.phone_addresses` (`sip.addresses`) | `trgw.min_port`–`max_port` (`10000`–`19999`), while `ibcf` is set |
| TrGW media, SIP trunks | Carriers | UDP | `trgw.trunk_addresses` (`ibcf.external.addresses`) | `trgw.min_port`–`max_port` (`10000`–`19999`), while `ibcf` is set |
| SIP: MCPTT, `direct` mode | MCPTT clients | UDP and TCP | `sip.addresses` | `mcptt.port` (`5090`) |
| MCPTT media | MCPTT clients | UDP | `mcptt.media.address` | `mcptt.media.min_port`–`max_port` (`20000`–`20999`), `mcptt.media.control_port` (`21000`) |
| Diameter: Cx, Rx, Sh, S6c, SGd | HSS, PCRF, SMSC, MME, SMSF | TCP or SCTP, set per peer | `diameter.address` | `diameter.port` (`3868`) to receive, over each transport a peer uses; each peer's port (`3868`) to connect |
| N5 | PCF | HTTP/2, optionally with mutual TLS | `n5.notify.address` | `n5.notify.port` (chosen by the system), while the policy interface is `n5` |

Defaults are in parentheses.

## Requirements

- **ESP**: firewalls between phones and Ella IMS must pass ESP (IP protocol 50).
- **`CAP_NET_ADMIN`**: Ella IMS sets up IPsec in the kernel. Run it as root, connect the snap's `network-control` interface, or add the capability to its container. On Kubernetes, only the `privileged` Pod Security Standard allows it.
- **SIP trunks**: expose only `ibcf.external.addresses` and `ibcf.external.port` to carriers. Never expose `ibcf.port` outside the core network: the IBCF accepts calls to SIP trunks there only from the I-CSCF, but it is the IMS side of the border. The IBCF ignores requests from sources outside the inbound networks of every SIP trunk, and answers 403 to a trunk that breaks its policy, such as a trusted trunk over UDP.
- **Routes**: Ella IMS needs a route to the phones' IP pools through the core network's N6 (5G) or SGi (4G) address.
