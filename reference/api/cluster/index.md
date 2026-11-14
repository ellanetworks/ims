# Cluster

A cluster is a set of Ella IMS nodes that share their settings, users and call records.

Reading the cluster requires Admin, Read Only or Network Manager. The other paths, except [Join a Cluster](#join-a-cluster), require Admin, and an access token: an API token gets `403`.

## Get the Cluster

This path returns the node that answered, its cluster, and every member.

| Method | Path              |
| ------ | ----------------- |
| GET    | `/api/v1/cluster` |

### Response Fields

| Field                       | Type    | Description                                                                            |
| --------------------------- | ------- | -------------------------------------------------------------------------------------- |
| `node_id`                   | string  | ID of the node that answered.                                                          |
| `cluster_id`                | string  | ID of the cluster.                                                                     |
| `clustered`                 | boolean | The node has a `cluster` block.                                                        |
| `leader_id`                 | string  | ID of the leader. Absent when there is none.                                           |
| `term`                      | int     | Election term.                                                                         |
| `applied_index`             | int     | Last log entry applied by the node.                                                    |
| `commit_index`              | int     | Last log entry committed by the cluster.                                               |
| `schema`                    | int     | Database schema version of the cluster.                                                |
| `healthy`                   | boolean | The cluster is healthy. Only from the leader.                                          |
| `failure_tolerance`         | int     | Voters that can fail without stopping writes. Only from the leader.                    |
| `members[].node_id`         | string  | ID of the member.                                                                      |
| `members[].address`         | string  | Cluster address of the member.                                                         |
| `members[].diameter_label`  | string  | First label of the member's Diameter host.                                             |
| `members[].diameter_host`   | string  | Diameter host of the member.                                                           |
| `members[].label_generated` | boolean | Whether `diameter_label` is the default, from `node_id`.                               |
| `members[].display_name`    | string  | Display name of the member. Empty when unset. See [Update a Member](#update-a-member). |
| `members[].api_address`     | string  | API address of the member.                                                             |
| `members[].version`         | string  | Version of the member.                                                                 |
| `members[].joined_at`       | string  | Time the member joined.                                                                |
| `members[].drain_state`     | string  | `active`, `draining` or `drained`. See [Drain a Member](#drain-a-member).              |
| `members[].suffrage`        | string  | `voter`, `nonvoter` (joining, or standby), or `none`.                                  |
| `members[].leader`          | boolean | The member is the leader.                                                              |
| `members[].healthy`         | boolean | The member is healthy. Only from the leader.                                           |
| `members[].last_contact`    | string  | Time since the leader last heard from the member. Only from the leader.                |

### Sample Response

```
{
    "result": {
        "node_id": "0199f6a2-5c1e-7d3a-9b1f-2a4c6e8d0f12",
        "cluster_id": "0199f6a2-4a10-7e55-8c3d-1b2d3e4f5a6b",
        "clustered": true,
        "leader_id": "0199f6a2-5c1e-7d3a-9b1f-2a4c6e8d0f12",
        "term": 4,
        "applied_index": 18342,
        "commit_index": 18342,
        "schema": 1,
        "healthy": true,
        "failure_tolerance": 1,
        "members": [
            {
                "node_id": "0199f6a2-5c1e-7d3a-9b1f-2a4c6e8d0f12",
                "address": "10.0.0.1:7000",
                "api_address": "https://10.0.0.1:5020",
                "diameter_label": "paris-1",
                "diameter_host": "paris-1.ims.mnc001.mcc001.3gppnetwork.org",
                "label_generated": false,
                "display_name": "paris-1",
                "version": "v0.9.0",
                "joined_at": "2026-11-05T09:12:44Z",
                "drain_state": "active",
                "suffrage": "voter",
                "leader": true,
                "healthy": true
            },
            {
                "node_id": "0199f6a3-0b77-7c21-a4e0-6f1e2d3c4b5a",
                "address": "10.0.0.2:7000",
                "api_address": "https://10.0.0.2:5020",
                "diameter_label": "ims-6f1e2d3c4b5a",
                "diameter_host": "ims-6f1e2d3c4b5a.ims.mnc001.mcc001.3gppnetwork.org",
                "label_generated": true,
                "display_name": "",
                "version": "v0.9.0",
                "joined_at": "2026-11-05T09:14:02Z",
                "drain_state": "active",
                "suffrage": "voter",
                "leader": false,
                "healthy": true,
                "last_contact": "0s"
            }
        ]
    }
}
```

## Create a Join Token

This path returns a token that lets one node join the cluster, once.

| Method | Path                     |
| ------ | ------------------------ |
| POST   | `/api/v1/cluster/tokens` |

### Request Body

| Field         | Type | Required | Description                                          |
| ------------- | ---- | -------- | ---------------------------------------------------- |
| `ttl_seconds` | int  | No       | Validity of the token, 300 to 86400. Default `1800`. |

### Sample Response

```
{
    "result": {
        "token": "AXsiaWQiOiI0ZjJk...",
        "expires_at": "2026-11-05T09:44:02Z"
    }
}
```

### Response Codes

| Code  | Description                            |
| ----- | -------------------------------------- |
| `201` | The token.                             |
| `400` | `ttl_seconds` is out of range.         |
| `409` | The node has no `cluster` block.       |
| `410` | The node was removed from the cluster. |

## Join a Cluster

This path joins a node with no user to a cluster. It takes no access token.

| Method | Path                   |
| ------ | ---------------------- |
| POST   | `/api/v1/cluster/join` |

### Request Body

| Field   | Type   | Required | Description                                               |
| ------- | ------ | -------- | --------------------------------------------------------- |
| `token` | string | Yes      | A token from [Create a Join Token](#create-a-join-token). |

### Sample Request

```
{
    "token": "AXsiaWQiOiI0ZjJk..."
}
```

### Sample Response

```
{
    "result": {
        "message": "Joined the cluster"
    }
}
```

### Response Codes

| Code  | Description                                                                                                                                |
| ----- | ------------------------------------------------------------------------------------------------------------------------------------------ |
| `400` | The token is not valid or expired, or the node runs an older version than the cluster.                                                     |
| `403` | The node has a user.                                                                                                                       |
| `409` | The node is already in a cluster, or has no `cluster` block, the token was used, or a peer or another member has the node's Diameter host. |
| `429` | Too many attempts from the client address.                                                                                                 |
| `502` | No member answered.                                                                                                                        |

## Update a Member

This path sets the display name of a member.

| Method | Path                                |
| ------ | ----------------------------------- |
| PUT    | `/api/v1/cluster/members/{node_id}` |

### Request Body

| Field          | Type   | Required | Description                      |
| -------------- | ------ | -------- | -------------------------------- |
| `display_name` | string | Yes      | Up to 63 bytes. Empty clears it. |

### Sample Request

```
{
    "display_name": "paris-1"
}
```

### Response Codes

| Code  | Description                            |
| ----- | -------------------------------------- |
| `400` | The name is over 63 bytes.             |
| `404` | No such member.                        |
| `410` | The node was removed from the cluster. |

## Drain a Member

| Method | Path                                       |
| ------ | ------------------------------------------ |
| POST   | `/api/v1/cluster/members/{node_id}/drain`  |
| POST   | `/api/v1/cluster/members/{node_id}/resume` |

`drain` sets the member's `drain_state` to `draining`, then `drained`. `resume` sets it to `active`.

| Interface  | Draining or drained member                                                        |
| ---------- | --------------------------------------------------------------------------------- |
| Readiness  | [Fails](https://docs.ellanetworks.com/ims/reference/api/status/#check-readiness). |
| Leadership | Moves to another voter.                                                           |
| P-CSCF     | Answers `REGISTER` with `305 Use Proxy`.                                          |

### Response Codes

| Code  | Description                                   |
| ----- | --------------------------------------------- |
| `404` | No such member.                               |
| `409` | The member is the only member of the cluster. |
| `410` | The node was removed from the cluster.        |

## Remove a Member

This path removes a drained member from the cluster. The removed node stops serving until its data directory is deleted.

| Method | Path                                |
| ------ | ----------------------------------- |
| DELETE | `/api/v1/cluster/members/{node_id}` |

### Query Parameters

| Name    | Type    | Description                                                                     |
| ------- | ------- | ------------------------------------------------------------------------------- |
| `force` | boolean | `true` removes a member that is not drained, such as one that is down for good. |

### Response Codes

| Code  | Description                                                                   |
| ----- | ----------------------------------------------------------------------------- |
| `404` | No such member.                                                               |
| `409` | The member is not drained, or the cluster would lose its majority without it. |
| `421` | The node is not the leader. See [Leader-Only Paths](#leader-only-paths).      |
| `503` | No leader. Retry.                                                             |

## Leader-Only Paths

[Remove a Member](#remove-a-member) and [Restore](https://docs.ellanetworks.com/ims/reference/api/backup/#restore) run on the leader only. Any other node answers `421`, with the leader's API address in `leader`.

| Field    | Type   | Description                                                 |
| -------- | ------ | ----------------------------------------------------------- |
| `error`  | string | The error.                                                  |
| `leader` | string | API address of the leader. Absent when the leader has none. |

### Sample Response

```
{
    "error": "This node is not the cluster leader: send the request to https://10.0.0.1:5020",
    "leader": "https://10.0.0.1:5020"
}
```
