---
description: Step-by-step instructions to back up Ella IMS.
---

# Back Up

Store and transfer backups encrypted.

## Back up

=== "Snap"

    1. Create an API token for an admin: in **Users**, open the user, and in **API Tokens**, click **Create Token**.

    2. Download a backup:

        ```shell
        curl -fOJ http://<host-ip>:5020/api/v1/backup \
          -H "Authorization: Bearer ellaims_..."
        ```

        This saves `ims-backup-<time>.tar.gz`.

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
