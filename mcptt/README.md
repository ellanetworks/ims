# Ella MCPTT (alpha)

> Interim, while MCPTT moves into Ella IMS (`../mcptt_migration.md`): commands run from the root of the ims repo.
> This page is folded into the ims README in step 7.

<p align="center">
  <img src="../docs/images/mcptt-logo-mark.svg" alt="Ella MCPTT Logo" width="120"/>
</p>

**Ella MCPTT** gives subscribers of a private 4G or 5G network push-to-talk group calls. It is a Mission Critical Push To Talk (MCPTT) server, an application server built into Ella IMS: off unless `ims.yaml` has an `mcptt` section.

## Key Features

- Prearranged group calls with floor control (TS 24.379, TS 24.380)
- Group affiliation
- Participating and controlling MCPTT functions, built into Ella IMS
- An application server of the IMS like any other: ISC, third-party registration
- Media relay for AMR-WB voice and the floor control channel
- Users, groups and service settings in the IMS's database, managed over its API
- Registrations and affiliations survive a restart

## How-to Guides

### Enable

In `ims.yaml`, then restart Ella IMS:

```yaml
mcptt:
  media:
    address: 10.0.0.5 # the address of its media relay, which the UEs reach
```

Then, in the HSS (Ella Core: an application server, and the filter criteria of the profile), the AS of server name
`sip:mcptt.<home domain>` and its iFCs. No PSI: the IMS routes its PSIs, `sip:mcptt-participating@mcptt.<home domain>`
and `sip:mcptt-controlling@mcptt.<home domain>`, itself. `GET /api/v1/status` has the name and the PSIs. The IMS
delivers what goes to that name to its MCPTT AS in memory.

### Provision users and groups

```sh
api=http://localhost:5020/api/v1/mcptt
curl -X POST $api/users -d '{"mcptt_id": "sip:alice@mcptt.example.com", "impus": ["sip:alice@ims.example.com"]}'
curl -X POST $api/groups -d '{"group_id": "sip:fire@mcptt.example.com", "invite_members": true,
  "maximum_duration_ms": 600000, "members": [{"mcptt_id": "sip:alice@mcptt.example.com"}]}'
curl -X PUT $api/users/sip:alice@mcptt.example.com -d '{"impus": ["sip:alice@ims.example.com"],
  "implicit_affiliations": ["sip:fire@mcptt.example.com"]}'
```

### Lab client

`mcpttc` registers with the AS in `direct` mode (`mcptt.mode: direct`), then reads commands from stdin (`call <group>`, `p`, `r`, `leave`, `quit`):

```sh
go run ./cmd/mcpttc -server 127.0.0.1:5090 -impu sip:alice@ims.example.com \
  -mcptt-id sip:alice@mcptt.example.com -groups sip:fire@mcptt.example.com
```

### Test

#### Unit and integration tests

```sh
go test ./...
```

#### End-to-end tests

The e2e tests of Ella IMS (`../e2e`) cover MCPTT (`e2e/mcptt_test.go`), with the iFCs of the MCPTT AS in `e2e/provision.js` and its users and groups in `e2e/mcptt/provision.sh`.

#### Load tests

1,000 registered users and 200 concurrent group calls of 100 members, in one process over loopback (Linux only):

```sh
ulimit -n 200000
go test ./internal/mcptt -run '^$' -bench '^BenchmarkRegistration$' -benchtime 1000x -count 3
go test ./internal/mcptt -run '^$' -bench '^BenchmarkGroupCalls$' -benchtime 1x -count 3
```

They report registrations and call setups per second, and the 50th, 90th and 99th percentiles of the MCPTT access time and of the server's share of the mouth-to-ear latency (TS 22.179 §6.15.3), in milliseconds.

#### Fuzz tests

```sh
(cd sip && go test -run='^$' -fuzz='^FuzzMcpttInfo$' ./mcpttinfo)
```

## Reference

### Configuration File

The `mcptt` section of [`ims.yaml`](../ims.yaml): `mode`, `port` (its SIP, in memory through the IMS, a socket in direct mode), and `media` (`address`, `advertise`, `min_port`, `max_port`, `control_port`).

### API

Under `/api/v1/mcptt/` of the IMS's API: see [`internal/api/openapi.yaml`](../internal/api/openapi.yaml). `GET /api/v1/status` has the AS's mode, name, PSIs and addresses.

### Modes

| `mcptt.mode` | Use | Registration |
| --- | --- | --- |
| `ims` | Production | Third-party REGISTER from the S-CSCF; requests accepted only from the IMS's own addresses |
| `direct` | Lab and tests only | The AS is the clients' registrar |

### IMS Core Requirements

- iFCs in the HSS that send the server:
  - a third-party REGISTER on REGISTER, with Include Register Request and Response;
  - originating requests with P-Asserted-Service `urn:urn-7:3gpp-service.ims.icsi.mcptt`.
- The S-CSCF sets P-Asserted-Service.
- The AS name of the iFCs is `sip:mcptt.<home domain>`, its PSIs those of the home domain: fixed, of the operator's MCC and MNC.

