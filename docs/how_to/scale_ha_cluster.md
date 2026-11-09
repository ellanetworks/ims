---
description: Add and remove nodes in a running Ella IMS high-availability cluster.
---

# Scale a High Availability Cluster

This guide adds a node to a running Ella IMS cluster, removes one, and replaces one whose database diverged. A cluster has at most 5 nodes.

## Prerequisites

- A running cluster, deployed as in [Deploy a High Availability Cluster](deploy_ha_cluster.md).
- An admin access token, from any node:

    ```shell
    TOKEN=$(curl -s http://<node>:5020/api/v1/auth/login \
      -H "Content-Type: application/json" \
      -d '{"email": "admin@example.com", "password": "..."}' | jq -r .result.token)
    ```

- The leader's API address:

    ```shell
    LEADER=$(curl -s http://<node>:5020/api/v1/cluster \
      -H "Authorization: Bearer $TOKEN" | jq -r '.result.members[] | select(.leader) | .api_address')
    ```

## Add a node

=== "Snap"

    1. Install Ella IMS on the new host, as in [Install](install.md), with a `cluster` block in the configuration file, as in [Deploy a High Availability Cluster](deploy_ha_cluster.md), step 1.
    2. Create a join token:

        ```shell
        JOIN=$(curl -s -X POST http://<node>:5020/api/v1/cluster/tokens \
          -H "Authorization: Bearer $TOKEN" | jq -r .result.token)
        ```

    3. Start Ella IMS on the new host:

        ```shell
        sudo snap start --enable ella-ims.imsd
        ```

    4. Join it to the cluster:

        ```shell
        curl http://<new-node>:5020/api/v1/cluster/join \
          -H "Content-Type: application/json" \
          -d "{\"token\": \"$JOIN\"}"
        ```

    5. On the HSS and the PCRF, accept the new node's Diameter host.
    6. Add the new node's SIP address to the P-CSCF addresses of the core network.
    7. Add the new node's `ibcf.external` address to each carrier of a SIP trunk.
    8. Check that the new node is ready:

        ```shell
        curl http://<new-node>:5020/api/v1/readyz
        ```

=== "Kubernetes"

    1. Add a Kubernetes node for the new pod.
    2. Add a pod:

        ```shell
        kubectl scale statefulset ella-ims -n ella --replicas=4
        ```

    3. On the HSS and the PCRF, accept the new pod's Diameter host, `ella-ims-3.ims.mnc<MNC>.mcc<MCC>.3gppnetwork.org`.
    4. Add the new pod's P-CSCF address to the P-CSCF addresses of the core network.
    5. Wait for the pod to be ready:

        ```shell
        kubectl wait --for=condition=Ready pod/ella-ims-3 -n ella --timeout=5m
        ```

## Remove a node

=== "Snap"

    1. Find the node's ID:

        ```shell
        curl -s $LEADER/api/v1/cluster \
          -H "Authorization: Bearer $TOKEN" | jq '.result.members[] | {node_id, address, drain_state}'
        ```

    2. Remove its SIP address from the P-CSCF addresses of the core network, and its `ibcf.external` address from each carrier.
    3. Drain it:

        ```shell
        curl -X POST $LEADER/api/v1/cluster/members/<node-id>/drain \
          -H "Authorization: Bearer $TOKEN"
        ```

    4. Wait until its `drain_state` is `drained`, as in step 1.
    5. Remove it, on the leader:

        ```shell
        curl -X DELETE $LEADER/api/v1/cluster/members/<node-id> \
          -H "Authorization: Bearer $TOKEN"
        ```

        For a node that is down for good, skip steps 3 and 4, and add `?force=true` to the URL. If the answer is `421`, set `LEADER` to its `leader` field and send the request again.

    6. On the removed host, stop Ella IMS and delete its data:

        ```shell
        sudo snap stop --disable ella-ims.imsd
        sudo rm -rf /var/snap/ella-ims/common/ims.db* /var/snap/ella-ims/common/node.db* /var/snap/ella-ims/common/raft
        ```

    7. On the HSS and the PCRF, remove the node's Diameter host.

=== "Kubernetes"

    1. Open the API:

        ```shell
        kubectl port-forward -n ella pod/ella-ims-0 5020:5020
        ```

    2. Find the ID of the pod with the highest number:

        ```shell
        curl -s http://localhost:5020/api/v1/cluster \
          -H "Authorization: Bearer $TOKEN" | jq '.result.members[] | {node_id, diameter_label, drain_state, leader}'
        ```

    3. Remove its P-CSCF address from the P-CSCF addresses of the core network.
    4. Drain it:

        ```shell
        curl -X POST http://localhost:5020/api/v1/cluster/members/<node-id>/drain \
          -H "Authorization: Bearer $TOKEN"
        ```

    5. Wait until its `drain_state` is `drained`, as in step 2.
    6. If `ella-ims-0` is not the leader, stop the port-forward, and open the API of the leader's pod, the member with `leader` `true` in step 2, whose `diameter_label` is the pod's name:

        ```shell
        kubectl port-forward -n ella pod/<leader-pod> 5020:5020
        ```

    7. Remove the drained member:

        ```shell
        curl -X DELETE http://localhost:5020/api/v1/cluster/members/<node-id> \
          -H "Authorization: Bearer $TOKEN"
        ```

    8. Remove its pod and volume:

        ```shell
        kubectl scale statefulset ella-ims -n ella --replicas=3
        kubectl delete pvc data-ella-ims-3 -n ella
        ```

    9. On the HSS and the PCRF, remove the pod's Diameter host.

## Replace a diverged node

A node whose database differs from the leader's fails its `consistent` [readiness check](../reference/api/status.md#check-readiness), and increments `ellaims_cluster_state_mismatches_total`.

=== "Snap"

    1. Remove the node, as in [Remove a node](#remove-a-node), steps 1 and 3 to 6.
    2. Add it again, as in [Add a node](#add-a-node), steps 2 to 4 and 8.

=== "Kubernetes"

    1. Drain and remove the node, as in [Remove a node](#remove-a-node), steps 1, 2 and 4 to 7.
    2. Delete its volume and pod:

        ```shell
        kubectl delete pvc data-ella-ims-<n> -n ella --wait=false
        kubectl delete pod ella-ims-<n> -n ella
        ```

    3. Wait for the pod to be ready:

        ```shell
        kubectl wait --for=condition=Ready pod/ella-ims-<n> -n ella --timeout=5m
        ```
