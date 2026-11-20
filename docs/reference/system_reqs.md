---
description: System requirements for running Ella IMS.
---

# System Requirements

Ella IMS runs on Linux. It supports **amd64** and **arm64** architectures.

|  | Up to 1,000 subscribers | Up to 50,000 subscribers |
|------------|-------------|-------------|
| **CPU** | 2 cores | 16 cores |
| **Memory** | 4 GB RAM | 16 GB RAM |
| **Disk** | SSD, 3,000 write IOPS | SSD, 3,000 write IOPS |
| **Operating System** | Ubuntu 26.04 LTS | Ubuntu 26.04 LTS |
| **Kernel** | IPsec: `xfrm_user`, `esp4`, `esp6` for IPv6, and `authenc`. SCTP, for Diameter peers over SCTP. | IPsec: `xfrm_user`, `esp4`, `esp6` for IPv6, and `authenc`. SCTP, for Diameter peers over SCTP. |
| **Network** | Incoming traffic processed on all CPUs | Incoming traffic processed on all CPUs |
| **Privileges** | `CAP_NET_ADMIN` | `CAP_NET_ADMIN` |

Both are measured, with room for registration bursts and calls: see the [performance reference](performance.md).

## Kubernetes

Ella IMS should work on most Kubernetes distributions. It has been validated on **RKE2** and **kind**.

| Requirement | Value |
|-------------|-------|
| **Nodes** | Meet the host requirements above, with the SCTP kernel module |
| **SIP network** | Phones reach the pod's SIP address directly, on a second pod interface. The Helm chart's default uses [Multus](https://github.com/k8snetworkplumbingwg/multus-cni) with the `macvlan` and `static` [CNI plugins](https://github.com/containernetworking/plugins). |
| **Pod Security** | `privileged` |
| **Capabilities** | `NET_ADMIN`, as root |
| **Storage** | A default StorageClass with `ReadWriteOnce` volumes, one per pod |
| **High availability** | Three nodes, and a second pod interface with its own SIP address for each pod |
