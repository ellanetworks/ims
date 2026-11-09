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

Restore the backup with an admin access token:

```shell
curl http://<host-ip>:5020/api/v1/restore \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/gzip" \
  --data-binary @ims-backup-<time>.tar.gz
```

In a cluster, send it to the leader. Another node answers `421` with the leader's address.

## Disaster recovery

If a cluster lost a majority of its nodes:

1. Deploy a new cluster, as in [Deploy a High Availability Cluster](deploy_ha_cluster.md), with empty data directories.
2. Restore the backup on its first node, as in [Restore](#restore).
