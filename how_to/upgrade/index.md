# Upgrade

## Prerequisites

- Ella IMS installed with [Install](https://docs.ellanetworks.com/ims/how_to/install/index.md) or [Deploy a High Availability Cluster](https://docs.ellanetworks.com/ims/how_to/deploy_ha_cluster/index.md).
- The target release of `ella-ims` available.

## 1. Upgrade

Refresh the snap:

```
sudo snap refresh ella-ims
```

In the directory of `docker-compose.yaml`, pull the new image and restart the container:

```
docker compose pull
docker compose up -d
```

1. Set the new image:

   ```
   kubectl set image statefulset/ella-ims -n ella ims=ghcr.io/ellanetworks/ella-ims:<version>
   ```

1. Wait for the rollout:

   ```
   kubectl rollout status statefulset/ella-ims -n ella
   ```

Repeat these steps for each node:

1. On that host, refresh the snap:

   ```
   sudo snap refresh ella-ims
   ```

1. On the leader's **Cluster** page, wait for the node to return to **Healthy**.

1. Set the new image:

   ```
   kubectl set image statefulset/ella-ims -n ella ims=ghcr.io/ellanetworks/ella-ims:<version>
   ```

1. Wait for the rollout:

   ```
   kubectl rollout status statefulset/ella-ims -n ella
   ```

## 2. Verify

Check the running version in the UI's top bar.

On the leader's **Cluster** page, confirm:

- Every node's **Version** column shows the target release.
- Every node is **Healthy** and its **Drain** column shows **Active**.
