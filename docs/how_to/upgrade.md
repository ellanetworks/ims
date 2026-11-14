---
description: Upgrade Ella IMS to the next release, on a standalone node or a high-availability cluster.
---

# Upgrade

## Prerequisites

- Ella IMS installed with [Install](install.md) or [Deploy a High Availability Cluster](deploy_ha_cluster.md).
- The target release of `ella-ims` available.

## 1. Upgrade

=== "Standalone"

    === "Snap"

        Refresh the snap:

        ```shell
        sudo snap refresh ella-ims
        ```

    === "Docker"

        In the directory of `docker-compose.yaml`, pull the new image and restart the container:

        ```shell
        docker compose pull
        docker compose up -d
        ```

    === "Kubernetes"

        1. Set the new image:

            ```shell
            kubectl set image statefulset/ella-ims -n ella ims=ghcr.io/ellanetworks/ella-ims:<version>
            ```

        2. Wait for the rollout:

            ```shell
            kubectl rollout status statefulset/ella-ims -n ella
            ```

=== "High Availability"

    === "Snap"

        Repeat these steps for each node:

        1. On that host, refresh the snap:

            ```shell
            sudo snap refresh ella-ims
            ```

        2. On the leader's **Cluster** page, wait for the node to return to **Healthy**.

    === "Kubernetes"

        1. Set the new image:

            ```shell
            kubectl set image statefulset/ella-ims -n ella ims=ghcr.io/ellanetworks/ella-ims:<version>
            ```

        2. Wait for the rollout:

            ```shell
            kubectl rollout status statefulset/ella-ims -n ella
            ```

## 2. Verify

=== "Standalone"

    Check the running version in the UI's top bar.

=== "High Availability"

    On the leader's **Cluster** page, confirm:

    - Every node's **Version** column shows the target release.
    - Every node is **Healthy** and its **Drain** column shows **Active**.
