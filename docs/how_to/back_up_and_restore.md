---
description: Step-by-step instructions to back up and restore Ella IMS.
---

# Back Up and Restore

Store and transfer backups encrypted.

## Back up

=== "Snap"

    1. Create an API token for an admin: in **Users**, open the user, and in **API Tokens**, click **Create Token**.

    2. Download a backup:

        ```shell
        curl -fOJ http://<host-ip>:5020/api/v1/backup \
          -H "Authorization: Bearer ellaims_..."
        ```

        This saves `ims-backup-<time>.tar.gz`. In a cluster, back up from any node.

=== "Kubernetes"

    1. Create an API token for an admin: in **Users**, open the user, and in **API Tokens**, click **Create Token**.

    2. Open the API:

        ```shell
        kubectl port-forward -n ella svc/ella-ims 5020:5020
        ```

    3. Download a backup:

        ```shell
        curl -fOJ http://localhost:5020/api/v1/backup \
          -H "Authorization: Bearer ellaims_..."
        ```

        This saves `ims-backup-<time>.tar.gz`.

## Restore

=== "Snap"

    1. Log in as an admin, and keep the token:

        ```shell
        TOKEN=$(curl -s http://<host-ip>:5020/api/v1/auth/login \
          -H "Content-Type: application/json" \
          -d '{"email": "admin@example.com", "password": "..."}' | jq -r .result.token)
        ```

    2. Restore the backup:

        ```shell
        curl http://<host-ip>:5020/api/v1/restore \
          -H "Authorization: Bearer $TOKEN" \
          -H "Content-Type: application/gzip" \
          --data-binary @ims-backup-<time>.tar.gz
        ```

        In a cluster, restore on the leader. Another node answers `421` with the leader's address.

=== "Kubernetes"

    1. Open the API of the leader's pod, named on the **Cluster** page, or `ella-ims-0` for a standalone node:

        ```shell
        kubectl port-forward -n ella pod/ella-ims-<n> 5020:5020
        ```

    2. Log in as an admin, and keep the token:

        ```shell
        TOKEN=$(curl -s http://localhost:5020/api/v1/auth/login \
          -H "Content-Type: application/json" \
          -d '{"email": "admin@example.com", "password": "..."}' | jq -r .result.token)
        ```

    3. Restore the backup:

        ```shell
        curl http://localhost:5020/api/v1/restore \
          -H "Authorization: Bearer $TOKEN" \
          -H "Content-Type: application/gzip" \
          --data-binary @ims-backup-<time>.tar.gz
        ```

## Disaster recovery

Use this procedure when a cluster lost a majority of its nodes, or a standalone node was lost.

=== "Snap"

    1. Install Ella IMS on a new host, as in [Install](install.md). For a cluster, add a `cluster` block to the configuration file.
    2. Open `http://<host-ip>:5020` in a browser and initialize it with a temporary user.
    3. Restore the backup with that user, as in [Restore](#restore).
    4. For a standalone deployment, stop here.
    5. Log in with a user from the backup, and add the other nodes, as in [Deploy a High Availability Cluster](deploy_ha_cluster.md), steps 2 to 4. Delete the data of any node that was in the old cluster first:

        ```shell
        sudo snap stop ella-ims.imsd
        sudo rm -rf /var/snap/ella-ims/common/ims.db* /var/snap/ella-ims/common/node.db* /var/snap/ella-ims/common/raft
        sudo snap start ella-ims.imsd
        ```

=== "Kubernetes"

    1. Delete the cluster and its volumes:

        ```shell
        kubectl delete -k ims/k8s/ha
        kubectl delete pvc -n ella -l app.kubernetes.io/name=ella-ims
        ```

    2. Deploy it again:

        ```shell
        kubectl apply -k ims/k8s/ha
        ```

    3. Initialize it with a temporary user, as in [Deploy a High Availability Cluster](deploy_ha_cluster.md), step 2.
    4. Restore the backup with that user, as in [Restore](#restore).
