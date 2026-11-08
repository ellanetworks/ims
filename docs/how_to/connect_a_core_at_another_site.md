---
description: Step-by-step instructions to connect Ella IMS to a core at another site.
---

# Connect a Core at Another Site

This guide connects Ella IMS, on Kubernetes or elsewhere, to a core, such as Ella Core, at another site.

## Prerequisites

- **Ella IMS**: installed and started, as in [Install](install.md).
- **For SMS over IP**: the HSS, and the MME or SMSF, support SCTP for Diameter.

The examples use these addresses:

| Address | Role |
|---|---|
| `10.6.0.5` | Ella IMS's P-CSCF address |
| `10.4.0.12` | Ella IMS's Diameter address, the pod's address on Kubernetes |
| `10.3.0.2` | Diameter address of the core's HSS and PCRF |
| `10.46.0.0/16` | IP pool of the core's `ims` data network |

## 1. Link the two sites

Set up a routed link, such as an IPsec VPN like [Google Cloud VPN](https://cloud.google.com/network-connectivity/docs/vpn/concepts/overview), between the core's site and the network of `10.6.0.5`. Keep NAT out of the path from the core's N6 or SGi interface to `10.6.0.5`.

## 2. Route the phones' traffic

- At the core's site, route `10.6.0.5` from the core's N6 or SGi interface through the link.
- On Ella IMS's side, route `10.46.0.0/16` through the link.

## 3. Allow SIP and ESP

From `10.46.0.0/16` to `10.6.0.5`, allow:

- UDP and TCP port `5060`.
- ESP, carrying UDP and TCP ports `5063`, `5064` and `5065`.

## 4. Route and allow Diameter

- At the core's site, route `10.4.0.12` through the link.
- On Ella IMS's side, route `10.3.0.2` through the link.
- Allow TCP or SCTP port `3868` from `10.4.0.12` to `10.3.0.2`. For SMS over IP, allow SCTP.

## 5. Point the core and Ella IMS at each other

- In the core, set the P-CSCF address to `10.6.0.5`. In Ella Core, it is **Operator → IMS → P-CSCF Addresses**.
- In Ella IMS, add `10.3.0.2` as the HSS and PCRF peer, as in [Integrate with Ella Core](integrate_with_ella_core.md), step 4.

## 6. Verify

In the Ella IMS UI, go to the **Cores** page. Under **Peers**, the core shows **open**.

Register a phone and make a call.
