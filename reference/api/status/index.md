# Status

## Get the Status

This path returns the version of the IMS, whether it has been initialized, and the identity of the node that answered. It takes no token.

| Method | Path             |
| ------ | ---------------- |
| GET    | `/api/v1/status` |

### Response Fields

| Field             | Type    | Description                                                                                                                                  |
| ----------------- | ------- | -------------------------------------------------------------------------------------------------------------------------------------------- |
| `version`         | string  | Version.                                                                                                                                     |
| `revision`        | string  | Git commit of the build. Empty in development builds.                                                                                        |
| `initialized`     | boolean | Whether the first user exists. Until then, [initialize](https://docs.ellanetworks.com/ims/reference/api/initialize/index.md) Ella IMS.       |
| `clustered`       | boolean | Whether the configuration file has a `cluster` block.                                                                                        |
| `node_id`         | string  | ID of the node that answered.                                                                                                                |
| `display_name`    | string  | Display name of the node. Empty when unset. See [Update a Member](https://docs.ellanetworks.com/ims/reference/api/cluster/#update-a-member). |
| `diameter_label`  | string  | First label of the node's Diameter host.                                                                                                     |
| `diameter_host`   | string  | Diameter host of the node.                                                                                                                   |
| `label_generated` | boolean | Whether `diameter_label` is the default, from `node_id`.                                                                                     |
| `formation`       | object  | See [Formation](#formation).                                                                                                                 |

### Sample Response

```
{
    "result": {
        "version": "v0.0.1",
        "revision": "",
        "initialized": true,
        "clustered": false,
        "node_id": "01a1ac75-e354-762b-b38f-013c0a7ca824",
        "display_name": "",
        "diameter_label": "ims-013c0a7ca824",
        "diameter_host": "ims-013c0a7ca824.ims.mnc001.mcc001.3gppnetwork.org",
        "label_generated": true,
        "formation": {
            "state": "member"
        }
    }
}
```

### Formation

| Field      | Type   | Description                                    |
| ---------- | ------ | ---------------------------------------------- |
| `state`    | string | `forming`, `joining`, `member` or `removed`.   |
| `expected` | int    | `cluster.bootstrap_expect`. Absent when unset. |
| `found`    | int    | Nodes found, this one included. While forming. |
| `detail`   | string | What the node is waiting for.                  |

## Check Liveness

This path answers while the process serves HTTP. It takes no token.

| Method | Path            |
| ------ | --------------- |
| GET    | `/api/v1/livez` |

It answers `204 No Content`.

## Check Readiness

This path returns whether the node is ready to receive traffic, and the result of each check. It takes no token.

| Method | Path             |
| ------ | ---------------- |
| GET    | `/api/v1/readyz` |

### Query Parameters

| Name      | Type    | Description                               |
| --------- | ------- | ----------------------------------------- |
| `cluster` | boolean | Add the `cluster` check. Default `false`. |

### Checks

| Name             | Passes when                                                                 |
| ---------------- | --------------------------------------------------------------------------- |
| `shutdown`       | The node is not stopping.                                                   |
| `member`         | The node was not removed from the cluster, and is not forming one.          |
| `initialized`    | The node has no `cluster` block, or a user exists.                          |
| `diameter_label` | The cluster recorded this node's Diameter host label.                       |
| `drain`          | The node is not draining or drained.                                        |
| `database`       | The database answers.                                                       |
| `leader`         | The node leads, or heard from the leader within twice the election timeout. |
| `cluster`        | The node leads, and every voter is healthy. Only with `cluster=true`.       |

### Response Fields

| Field             | Type    | Description                         |
| ----------------- | ------- | ----------------------------------- |
| `ready`           | boolean | Whether every check passes.         |
| `checks`          | array   | The checks, in order.               |
| `checks[].name`   | string  | Name of the check.                  |
| `checks[].ok`     | boolean | Whether it passes.                  |
| `checks[].detail` | string  | Why it fails. Empty when it passes. |

### Response Codes

| Code  | Description                         |
| ----- | ----------------------------------- |
| `200` | Every check passes.                 |
| `400` | `cluster` is not `true` or `false`. |
| `503` | A check fails.                      |

### Sample Response

```
{
    "result": {
        "ready": false,
        "checks": [
            {"name": "shutdown", "ok": true, "detail": ""},
            {"name": "member", "ok": true, "detail": ""},
            {"name": "initialized", "ok": true, "detail": ""},
            {"name": "diameter_label", "ok": true, "detail": ""},
            {"name": "drain", "ok": true, "detail": ""},
            {"name": "database", "ok": true, "detail": ""},
            {"name": "leader", "ok": false, "detail": "no contact with the cluster leader for 12s"}
        ]
    }
}
```
