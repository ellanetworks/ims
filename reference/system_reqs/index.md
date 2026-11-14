# System Requirements

Ella IMS runs on Linux. It supports **amd64** and **arm64** architectures.

|                      | Up to 1,000 subscribers                                                                         | Up to 50,000 subscribers                                                                        |
| -------------------- | ----------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------- |
| **CPU**              | 2 cores                                                                                         | 16 cores                                                                                        |
| **Memory**           | 4 GB RAM                                                                                        | 16 GB RAM                                                                                       |
| **Disk**             | SSD, 3,000 write IOPS                                                                           | SSD, 3,000 write IOPS                                                                           |
| **Operating System** | Ubuntu 26.04 LTS                                                                                | Ubuntu 26.04 LTS                                                                                |
| **Kernel**           | IPsec: `xfrm_user`, `esp4`, `esp6` for IPv6, and `authenc`. SCTP, for Diameter peers over SCTP. | IPsec: `xfrm_user`, `esp4`, `esp6` for IPv6, and `authenc`. SCTP, for Diameter peers over SCTP. |
| **Network**          | Incoming traffic processed on all CPUs                                                          | Incoming traffic processed on all CPUs                                                          |
| **Privileges**       | `CAP_NET_ADMIN`                                                                                 | `CAP_NET_ADMIN`                                                                                 |
| **Kubernetes nodes** | SCTP kernel module                                                                              | SCTP kernel module                                                                              |

Both are measured, with room for registration bursts and calls: see the [performance reference](https://docs.ellanetworks.com/ims/reference/performance/index.md).
