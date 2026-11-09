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

    2. In a cluster, find the leader's API address:

        ```shell
        curl -s http://<host-ip>:5020/api/v1/cluster \
          -H "Authorization: Bearer $TOKEN" | jq -r '.result.members[] | select(.leader) | .api_address'
        ```

    3. Restore the backup, on the leader in a cluster:

        ```shell
        curl <api-address>/api/v1/restore \
          -H "Authorization: Bearer $TOKEN" \
          -H "Content-Type: application/gzip" \
          --data-binary @ims-backup-<time>.tar.gz
        ```

        If the answer is `421`, send the request again to the address in its `leader` field.

=== "Kubernetes"

    1. Open the API of the leader's pod, the first pod in a standalone deployment:

        ```shell
        kubectl port-forward -n ella pod/ella-ims-0 5020:5020
        ```

    2. Log in as an admin, and keep the token:

        ```shell
        TOKEN=$(curl -s http://localhost:5020/api/v1/auth/login \
          -H "Content-Type: application/json" \
          -d '{"email": "admin@example.com", "password": "..."}' | jq -r .result.token)
        ```

    3. In a cluster, check that the pod is the leader:

        ```shell
        curl -s http://localhost:5020/api/v1/cluster \
          -H "Authorization: Bearer $TOKEN" | jq -r '.result.members[] | select(.leader) | .diameter_label'
        ```

        This prints the leader's pod name. If it is another pod, stop the port-forward and repeat from step 1 with that pod.

    4. Restore the backup:

        ```shell
        curl http://localhost:5020/api/v1/restore \
          -H "Authorization: Bearer $TOKEN" \
          -H "Content-Type: application/gzip" \
          --data-binary @ims-backup-<time>.tar.gz
        ```

## Disaster recovery

Use this procedure when a cluster lost a majority of its nodes, or a standalone node was lost.

=== "Snap"

    1. Install Ella IMS on a new host, as in [Install](install.md). For a cluster, add a `cluster` block to the configuration file, as in [Deploy a High Availability Cluster](deploy_ha_cluster.md), step 1.
    2. Open `http://<host-ip>:5020` in a browser and create a temporary user.
    3. Restore the backup with that user, as in [Restore](#restore).
    4. For a standalone deployment, stop here.
    5. Log in with a user from the backup, and add the other nodes, as in [Deploy a High Availability Cluster](deploy_ha_cluster.md), steps 3 to 5. Delete the data of any node that was in the old cluster first:

        ```shell
        sudo snap stop ella-ims.imsd
        sudo rm -rf /var/snap/ella-ims/common/ims.db* /var/snap/ella-ims/common/node.db* /var/snap/ella-ims/common/raft
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

    3. Create a temporary user, as in [Deploy a High Availability Cluster](deploy_ha_cluster.md), step 2.
    4. Restore the backup with that user, as in [Restore](#restore).
