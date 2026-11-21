# Performance

Measured capacity of the two hosts of the [system requirements](https://docs.ellanetworks.com/ims/reference/system_reqs/index.md).

## Registrations

Registration time: from the initial REGISTER to its 200 OK, through the IMS-AKA challenge, measured at the phone (TS 32.409 §4.1.1.1). Once all are registered, each phone deregisters, then registers again.

### 1,000 Subscribers, 2 vCPU

| Load                                | Rate (/s) | Initial registration success rate | Mean  | p99    | CPU (cores) |
| ----------------------------------- | --------- | --------------------------------- | ----- | ------ | ----------- |
| From 0 to 1,000 registered, in 10 s | 100       | 100%                              | 29 ms | 307 ms | —           |
| 1,000 registered                    | 10        | 100%                              | 9 ms  | 30 ms  | 0.21        |
| 1,000 registered                    | 25        | 100%                              | 8 ms  | 23 ms  | 0.52        |
| 1,000 registered                    | 50        | 100%                              | 19 ms | 41 ms  | 0.97        |

### 50,000 Subscribers, 16 vCPU

| Load                                       | Rate (/s) | Initial registration success rate | Mean  | p99    | CPU (cores) |
| ------------------------------------------ | --------- | --------------------------------- | ----- | ------ | ----------- |
| From 0 to 50,000 registered, in 8 min 20 s | 100       | 100%                              | 18 ms | 98 ms  | —           |
| 50,000 registered                          | 50        | 100%                              | 42 ms | 1.04 s | 0.93        |
| 50,000 registered                          | 100       | 100%                              | 51 ms | 1.07 s | 1.72        |
| 50,000 registered                          | 150       | 10%                               | 5.1 s | 23.6 s | 0.99        |

### Re-registrations

Each subscriber re-registers every 3,000 s (TS 24.229 §5.1.1.4.1), during the calls.

| Subscribers | Rate (/s) | Re-registration success rate | Mean  | p99    |
| ----------- | --------- | ---------------------------- | ----- | ------ |
| 1,000       | 0.33      | 100%                         | 6 ms  | 21 ms  |
| 50,000      | 16.7      | 100%                         | 27 ms | 122 ms |

## Calls

Voice calls with preconditions (IR.92 §2.4.1), answered at once and held 90 s. Session setup time: from the INVITE to the first 180 or 2xx, measured at the calling phone (TS 32.409 §4.1.2.6).

### 1,000 Subscribers, 2 vCPU

| Rate (/s) | Calls per hour | Session establishment success rate | Call drop rate | Session setup time, mean | p99   | Calls in progress | CPU (cores) | Resident memory |
| --------- | -------------- | ---------------------------------- | -------------- | ------------------------ | ----- | ----------------- | ----------- | --------------- |
| 2         | 7,200          | 100%                               | 0%             | 15 ms                    | 23 ms | 181               | 0.05        | 211 MiB         |
| 5         | 18,000         | 100%                               | 0%             | 16 ms                    | 31 ms | 451               | 0.10        | 267 MiB         |

### 50,000 Subscribers, 16 vCPU

| Rate (/s) | Calls per hour | Session establishment success rate | Call drop rate | Session setup time, mean | p99    | Calls in progress | CPU (cores) | Resident memory |
| --------- | -------------- | ---------------------------------- | -------------- | ------------------------ | ------ | ----------------- | ----------- | --------------- |
| 25        | 90,000         | 100%                               | 0%             | 54 ms                    | 155 ms | 2,255             | 0.61        | 4.0 GiB         |
| 50        | 180,000        | 100%                               | 0%             | 55 ms                    | 144 ms | 4,501             | 1.00        | 4.6 GiB         |
| 100       | 360,000        | 100%                               | 0%             | 41 ms                    | 126 ms | 9,009             | 1.78        | 6.0 GiB         |

## Memory

| Item                                      | Value                     |
| ----------------------------------------- | ------------------------- |
| Resident memory, idle                     | 30 MiB                    |
| Resident memory per registered subscriber | 71 KiB                    |
| Kernel memory per registered subscriber   | 26 KiB                    |
| Memory of ended calls                     | Released within 2 minutes |

## Limits

| Limit                  | Value                                                                                                 |
| ---------------------- | ----------------------------------------------------------------------------------------------------- |
| Registered subscribers | 50,000 tested                                                                                         |
| Registration rate      | Set by the disk's write IOPS: 100/s with 3,000, 150/s with 5,800. Beyond it, most registrations fail. |
| SIP TCP connections    | 3/4 of the open-file limit: 393,216 at systemd's default of 524,288. Each phone uses up to 2.         |
| MCPTT users            | 1,000                                                                                                 |

On Linux 6.13 and later, the kernel's processing of incoming IPsec traffic grows with the number of registered phones.

## Host Requirements

| Requirement    | Value                                                                                                                                                                                                     |
| -------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Receive queues | A receive queue per CPU, or, with a single receive queue, Receive Packet Steering (RPS) on all CPUs: `/sys/class/net/<interface>/queues/rx-0/rps_cpus`. Otherwise one CPU processes all incoming traffic. |

## Test Setup

| Item                     | Value                                                                                                                                                              |
| ------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Version                  | Ella IMS built from source, after v0.0.1, default configuration                                                                                                    |
| Host, 1,000 subscribers  | Google Cloud `n2-custom-2-4096`: 2 vCPU, 4 GB RAM                                                                                                                  |
| Host, 50,000 subscribers | Google Cloud `n2-highcpu-16`: 16 vCPU, 16 GB RAM                                                                                                                   |
| Operating system         | Ubuntu 26.04, Linux 7.0 (`7.0.0-1014-gcp`)                                                                                                                         |
| Network                  | `virtio_net`, one receive queue per vCPU                                                                                                                           |
| Disk                     | Google Cloud `pd-balanced`, 30 GB: 3,000 IOPS                                                                                                                      |
| Open-file limit          | 1,048,576                                                                                                                                                          |
| Load generator           | Separate VM in the same zone: `n2-standard-4` for 1,000 subscribers, `n2-standard-16` for 50,000                                                                   |
| Phones                   | Simulated: IMS-AKA, IPsec with integrity protection only (`hmac-sha-1-96`, no encryption), SIP over TCP, one IP address per phone, registration event subscription |
| Re-registrations         | Not re-authenticated (S-CSCF default)                                                                                                                              |
| HSS                      | Simulated, over Cx, on the load generator: 1.7 ms mean response time with 1,000 subscribers, 2.8 ms with 50,000                                                    |
| Policy                   | Simulated PCRF, over Rx, on the load generator: 0.6 ms mean response time with 1,000 subscribers, 3.8 ms with 50,000                                               |
| Media                    | Not carried by Ella IMS                                                                                                                                            |
