# Environment Variables

These environment variables override parameters of the configuration file.

| Variable                  | Overrides                                                                                                                                             |
| ------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| `IMS_DIAMETER_HOST_LABEL` | `diameter.host_label` in the [configuration file](https://docs.ellanetworks.com/ims/reference/config_file/index.md)                                   |
| `IMS_CLUSTER_ADVERTISE`   | `cluster.advertise` in the [configuration file](https://docs.ellanetworks.com/ims/reference/config_file/index.md). Ignored without a `cluster` block. |
