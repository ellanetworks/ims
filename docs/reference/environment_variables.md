---
description: Reference of the environment variables of Ella IMS.
---

# Environment Variables

These environment variables override parameters of the configuration file.

| Variable | Overrides |
| -------- | --------- |
| `IMS_DIAMETER_HOST_LABEL` | `diameter.host_label` in the [configuration file](config_file.md) |
| `IMS_CLUSTER_ADVERTISE` | `cluster.advertise` in the [configuration file](config_file.md). Ignored without a `cluster` block. |
