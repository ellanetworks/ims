---
description: Step-by-step instructions to upgrade Ella IMS.
---

# Upgrade

## Upgrade a standalone node

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

    3. Open the UI and API on your machine:

        ```shell
        kubectl port-forward -n ella svc/ella-ims 5020:5020
        ```

Check the running version in the UI's top bar.

## Upgrade a cluster

Upgrade one node at a time.

=== "Snap"

    Repeat these steps for each node:

    1. Refresh the snap:

        ```shell
        sudo snap refresh ella-ims
        ```

    2. Wait until the node is ready, when this answers `200`:

        ```shell
        curl http://<node>:5020/api/v1/readyz
        ```

=== "Kubernetes"

    1. Set the new image:

        ```shell
        kubectl set image statefulset/ella-ims -n ella ims=ghcr.io/ellanetworks/ella-ims:<version>
        ```

    2. Wait for the rollout, which replaces one pod at a time:

        ```shell
        kubectl rollout status statefulset/ella-ims -n ella
        ```

Check every node's version:

```shell
curl -s http://<node>:5020/api/v1/cluster \
  -H "Authorization: Bearer $TOKEN" | jq '.result.members[] | {address, version}'
```

`TOKEN` is an admin access token, from `POST /api/v1/auth/login`.
