---
description: Step-by-step instructions to deploy Ella IMS as a high-availability cluster.
---

# Deploy a High Availability Cluster

This guide deploys Ella IMS as a cluster of three nodes. See [High Availability](../explanation/high_availability.md) for how the cluster works.

=== "Snap"

    ## Prerequisites

    - Three hosts meeting the [system requirements](../reference/system_reqs.md), with Ella IMS installed as in [Install](install.md), not started.

    The examples use these addresses:

    | Node | Cluster and API address | SIP address | Diameter address |
    | ---- | ----------------------- | ----------- | ---------------- |
    | 1    | `10.0.0.1`              | `10.6.0.5`  | `10.3.0.5`       |
    | 2    | `10.0.0.2`              | `10.6.0.6`  | `10.3.0.6`       |
    | 3    | `10.0.0.3`              | `10.6.0.7`  | `10.3.0.7`       |

    ## 1. Configure the nodes

    On each host, add a `cluster` block with the host's address to `/var/snap/ella-ims/common/ims.yaml`, and set its own SIP and Diameter addresses. On node 1:

    ```yaml
    sip:
      addresses: [10.6.0.5]
    diameter:
      address: 10.3.0.5
    cluster:
      address: 10.0.0.1
      port: 7000
    ```

    ## 2. Start node 1

    Start Ella IMS on node 1:

    ```shell
    sudo snap start --enable ella-ims.imsd
    ```

    Open `http://10.0.0.1:5020`, create the first user, and connect it to the HSS and the PCRF, as in [Integrate with Ella Core](integrate_with_ella_core.md), steps 3 and 4.

    ## 3. Create a join token for node 2

    Log in to node 1 as an admin, and keep the token:

    ```shell
    TOKEN=$(curl -s http://10.0.0.1:5020/api/v1/auth/login \
      -H "Content-Type: application/json" \
      -d '{"email": "admin@example.com", "password": "..."}' | jq -r .result.token)
    ```

    Create a join token:

    ```shell
    JOIN=$(curl -s -X POST http://10.0.0.1:5020/api/v1/cluster/tokens \
      -H "Authorization: Bearer $TOKEN" | jq -r .result.token)
    ```

    ## 4. Join node 2

    Start Ella IMS on node 2:

    ```shell
    sudo snap start --enable ella-ims.imsd
    ```

    Join it to the cluster:

    ```shell
    curl http://10.0.0.2:5020/api/v1/cluster/join \
      -H "Content-Type: application/json" \
      -d "{\"token\": \"$JOIN\"}"
    ```

    ## 5. Join node 3

    Repeat steps 3 and 4 for node 3, at `10.0.0.3`.

    ## 6. Connect the Diameter peers

    List each node's Diameter host:

    ```shell
    curl -s http://10.0.0.1:5020/api/v1/cluster \
      -H "Authorization: Bearer $TOKEN" | jq '.result.members[] | {address, diameter_host}'
    ```

    On the HSS and the PCRF, accept every node's Diameter host, from its Diameter address.

    ## 7. Give the phones every P-CSCF

    In the core network, set the P-CSCF addresses to every node's SIP address. In Ella Core, set **Operator → IMS → P-CSCF Addresses** to `10.6.0.5`, `10.6.0.6` and `10.6.0.7`.

    ## 8. Connect the SIP trunks

    If Ella IMS has SIP trunks, configure each carrier with every node's `ibcf.external` address, or with one host name that resolves to all of them.

    ## 9. Verify

    Find the leader:

    ```shell
    curl -s http://10.0.0.1:5020/api/v1/cluster \
      -H "Authorization: Bearer $TOKEN" | jq -r '.result.members[] | select(.leader) | .api_address'
    ```

    Check the cluster on the leader, at that address:

    ```shell
    curl "<leader-api-address>/api/v1/readyz?cluster=true"
    ```

    It answers `200`, with every check `ok`.

=== "Kubernetes"

    ## Prerequisites

    - A Kubernetes cluster with three nodes meeting the [system requirements](../reference/system_reqs.md).
    - A second interface, `eth1`, on the network phones reach Ella IMS on, for each pod, as in [Install](install.md).

    ## 1. Deploy

    ```shell
    git clone --depth 1 https://github.com/ellanetworks/ims.git
    kubectl create namespace ella
    kubectl label namespace ella pod-security.kubernetes.io/enforce=privileged
    kubectl create secret generic ella-ims-cluster -n ella --from-literal=secret="$(openssl rand -base64 32)"
    kubectl apply -k ims/k8s/ha
    ```

    ## 2. Initialize

    ```shell
    kubectl port-forward -n ella pod/ella-ims-0 5020:5020
    ```

    Open `http://localhost:5020`, create the first user, and connect it to the HSS and the PCRF, as in [Integrate with Ella Core](integrate_with_ella_core.md), steps 3 and 4.

    ## 3. Connect the Diameter peers

    On the HSS and the PCRF, accept the Diameter host of every pod, `ella-ims-<n>.ims.mnc<MNC>.mcc<MCC>.3gppnetwork.org`, from its address.

    ## 4. Give the phones every P-CSCF

    Find each pod's P-CSCF address, the `pcscf` entry of `sip`:

    ```shell
    kubectl logs -n ella ella-ims-<n> | grep '"ims started"'
    ```

    In the core network, set the P-CSCF addresses to those of every pod.

    ## 5. Verify

    Log in as an admin, and keep the token:

    ```shell
    TOKEN=$(curl -s http://localhost:5020/api/v1/auth/login \
      -H "Content-Type: application/json" \
      -d '{"email": "admin@example.com", "password": "..."}' | jq -r .result.token)
    ```

    List the members:

    ```shell
    curl -s http://localhost:5020/api/v1/cluster \
      -H "Authorization: Bearer $TOKEN" | jq '.result.members[] | {display_name, diameter_label, suffrage, leader}'
    ```

    Three members appear, each with `suffrage` `voter`.
