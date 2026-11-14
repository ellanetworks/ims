# Back Up and Restore

Store and transfer backups encrypted.

## Back up

1. Create an API token for an admin: in **Users**, open the user, and in **API Tokens**, click **Create Token**.

1. Download a backup:

   ```
   curl -fOJ http://<host-ip>:5020/api/v1/backup \
     -H "Authorization: Bearer ellaims_..."
   ```

   This saves `ims-backup-<time>.tar.gz`. In a cluster, back up from any node.

1. Create an API token for an admin: in **Users**, open the user, and in **API Tokens**, click **Create Token**.

1. Open the API:

   ```
   kubectl port-forward -n ella svc/ella-ims 5020:5020
   ```

1. Download a backup:

   ```
   curl -fOJ http://localhost:5020/api/v1/backup \
     -H "Authorization: Bearer ellaims_..."
   ```

   This saves `ims-backup-<time>.tar.gz`.

## Restore

1. Log in as an admin, and keep the token:

   ```
   TOKEN=$(curl -s http://<host-ip>:5020/api/v1/auth/login \
     -H "Content-Type: application/json" \
     -d '{"email": "admin@example.com", "password": "..."}' | jq -r .result.token)
   ```

1. Restore the backup:

   ```
   curl http://<host-ip>:5020/api/v1/restore \
     -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/gzip" \
     --data-binary @ims-backup-<time>.tar.gz
   ```

   In a cluster, restore on the leader. Another node answers `421` with the leader's address.

1. Open the API of the leader's pod, named on the **Cluster** page, or `ella-ims-0` for a standalone node:

   ```
   kubectl port-forward -n ella pod/ella-ims-<n> 5020:5020
   ```

1. Log in as an admin, and keep the token:

   ```
   TOKEN=$(curl -s http://localhost:5020/api/v1/auth/login \
     -H "Content-Type: application/json" \
     -d '{"email": "admin@example.com", "password": "..."}' | jq -r .result.token)
   ```

1. Restore the backup:

   ```
   curl http://localhost:5020/api/v1/restore \
     -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/gzip" \
     --data-binary @ims-backup-<time>.tar.gz
   ```

## Disaster recovery

Use this procedure when a cluster lost a majority of its nodes, or a standalone node was lost.

1. Install Ella IMS on a new host, as in [Install](https://docs.ellanetworks.com/ims/how_to/install/index.md). For a cluster, add a `cluster` block to the configuration file.

1. Open `http://<host-ip>:5020` in a browser and initialize it with a temporary user.

1. Restore the backup with that user, as in [Restore](#restore).

1. For a standalone deployment, stop here.

1. Log in with a user from the backup, and add the other nodes, as in [Deploy a High Availability Cluster](https://docs.ellanetworks.com/ims/how_to/deploy_ha_cluster/index.md), steps 2 to 4. Delete the data of any node that was in the old cluster first:

   ```
   sudo snap stop ella-ims.imsd
   sudo rm -rf /var/snap/ella-ims/common/ims.db* /var/snap/ella-ims/common/node.db* /var/snap/ella-ims/common/raft
   sudo snap start ella-ims.imsd
   ```

1. Delete the cluster and its volumes:

   ```
   kubectl delete -k ims/k8s/ha
   kubectl delete pvc -n ella -l app.kubernetes.io/name=ella-ims
   ```

1. Deploy it again:

   ```
   kubectl apply -k ims/k8s/ha
   ```

1. Initialize it with a temporary user, as in [Deploy a High Availability Cluster](https://docs.ellanetworks.com/ims/how_to/deploy_ha_cluster/index.md), step 2.

1. Restore the backup with that user, as in [Restore](#restore).
