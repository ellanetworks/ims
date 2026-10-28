# Performance

Measured registration and call capacity of Ella IMS, with 20,000 registered subscribers on a 4 vCPU host.

## Registrations

Registration time: from the initial REGISTER to its 200 OK, through the IMS-AKA challenge, measured at the phone (TS 32.409 §4.1.1.1).

| Load                                       | Rate (/s) | Initial registration success rate | Mean  | p99    | CPU (cores) |
| ------------------------------------------ | --------- | --------------------------------- | ----- | ------ | ----------- |
| From 0 to 20,000 registered, in 3 min 20 s | 100       | 100%                              | 21 ms | 81 ms  | —           |
| 20,000 registered                          | 50        | 100%                              | 32 ms | 127 ms | 0.58        |
| 20,000 registered                          | 100       | 100%                              | 56 ms | 1.04 s | 1.19        |
| 20,000 registered                          | 150       | 100%                              | 2.6 s | 4.5 s  | 1.43        |

| Re-registration              | Value                                                                      |
| ---------------------------- | -------------------------------------------------------------------------- |
| Rate                         | 6.7/s: each of the 20,000 subscribers every 3,000 s (TS 24.229 §5.1.1.4.1) |
| Re-registration success rate | 100%                                                                       |
| Mean                         | 19 ms                                                                      |
| p99                          | 97 ms                                                                      |

## Calls

Voice calls with preconditions (IR.92 §2.4.1), answered at once and held 90 s. Session setup time: from the INVITE to the first 180 or 2xx, measured at the calling phone (TS 32.409 §4.1.2.6).

| Rate (/s) | Calls per hour | Session establishment success rate | Call drop rate | Session setup time, mean | p99    | Calls in progress | CPU (cores) | Resident memory |
| --------- | -------------- | ---------------------------------- | -------------- | ------------------------ | ------ | ----------------- | ----------- | --------------- |
| 25        | 90,000         | 100%                               | 0%             | 42 ms                    | 129 ms | 1,499             | 0.26        | 1.8 GiB         |
| 50        | 180,000        | 100%                               | 0%             | 57 ms                    | 155 ms | 2,999             | 0.47        | 2.0 GiB         |
| 100       | 360,000        | 100%                               | 0%             | 59 ms                    | 150 ms | 6,000             | 0.93        | 2.7 GiB         |

## Memory

| Item                                      | Value                     |
| ----------------------------------------- | ------------------------- |
| Resident memory, idle                     | 25 MiB                    |
| Resident memory per registered subscriber | 76 KiB                    |
| Memory of ended calls                     | Released within 2 minutes |

## Limits

| Limit                  | Value                                                                                         |
| ---------------------- | --------------------------------------------------------------------------------------------- |
| Registered subscribers | 20,000 tested                                                                                 |
| SIP TCP connections    | 3/4 of the open-file limit: 393,216 at systemd's default of 524,288. Each phone uses up to 2. |
| MCPTT users            | 1,000                                                                                         |

## Host Requirements

| Requirement                   | Value                                                                                                                                                                   |
| ----------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Receive Packet Steering (RPS) | On all CPUs, on a network interface with a single receive queue: `/sys/class/net/<interface>/queues/rx-0/rps_cpus`. Without it, one CPU processes all incoming traffic. |
| Linux 6.13 and later          | The kernel's processing of incoming IPsec traffic grows with the number of registered phones per receiving CPU.                                                         |

## Test Setup

| Item             | Value                                                                                                                                                              |
| ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Version          | Ella IMS built from source, after v0.0.1, default configuration                                                                                                    |
| Host             | VM, 4 vCPU, 8 GB RAM, Ubuntu 26.04, Linux 7.0, on an Intel Core Ultra 7 265K                                                                                       |
| Network          | One receive queue, RPS on all 4 CPUs                                                                                                                               |
| Open-file limit  | 524,288 (systemd default)                                                                                                                                          |
| Load generator   | Separate VM, 10 vCPU, 10 GB RAM                                                                                                                                    |
| Phones           | Simulated: IMS-AKA, IPsec with integrity protection only (`hmac-sha-1-96`, no encryption), SIP over TCP, one IP address per phone, registration event subscription |
| Re-registrations | Not re-authenticated (S-CSCF default)                                                                                                                              |
| HSS              | Simulated, over Cx: 2.5 ms mean response time                                                                                                                      |
| Policy           | Simulated PCRF, over Rx: 3.6 ms mean response time                                                                                                                 |
| Media            | Not carried by Ella IMS                                                                                                                                            |
