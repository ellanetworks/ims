---
description: Step-by-step instructions to upgrade Ella IMS.
---

# Upgrade

Ella IMS restarts on the new version and keeps its database.

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

The UI shows the running version in its top bar.
