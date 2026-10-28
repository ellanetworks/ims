---
description: System requirements for running Ella IMS.
---

# System Requirements

Ella IMS runs on Linux. It supports **amd64** and **arm64** architectures.

|  | Production |
|------------|-------------|
| **CPU** | 4 cores |
| **Memory** | 8 GB RAM |
| **Operating System** | Ubuntu 26.04 LTS |
| **Kernel** | IPsec: `xfrm_user`, `esp4`, `esp6` for IPv6, and `authenc`. SCTP, for Diameter peers over SCTP. |
| **Privileges** | `CAP_NET_ADMIN` (see the [connectivity reference](connectivity.md)) |

The production host is the one of the [performance reference](performance.md): 20,000 registered subscribers.
