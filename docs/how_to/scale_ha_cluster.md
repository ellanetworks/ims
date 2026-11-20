---
description: Add and remove nodes in a running Ella IMS high-availability cluster.
---

# Scale a High Availability Cluster

This guide walks through adding a node to an existing Ella IMS high-availability cluster, and removing one.

## Prerequisites

- A running HA cluster deployed (see [Deploy a High Availability Cluster](deploy_ha_cluster.md)).
- On Kubernetes, the `values.yaml` of the release, and its chart version, `<version>`, from `helm list -n ella`.

## Add a node

=== "Snap"

    1. Prepare a host with Ella IMS installed per the [Install](install.md) guide, with a `cluster` block in the configuration file.
    2. Open the **Cluster** page on any node in the cluster.
    3. Click **Add Node**, click **Create Token**, and copy the token.
    4. Start Ella IMS on the new host.
    5. Open the new node's UI in a browser. Click **Join an existing cluster instead**, paste the token, and click **Join**.
    6. On the HSS and the PCRF, accept the new node's Diameter host. Add its SIP address to the P-CSCF addresses of the core network.
    7. On the leader's **Cluster** page, verify the new node appears and is shown as **Healthy**.

=== "Kubernetes"

    1. Add a Kubernetes node for the new pod, with a second interface, as for the others.
    2. In `values.yaml`, set `replicas` to `4`, and upgrade the release:

        ```shell
        helm upgrade ella-ims oci://ghcr.io/ellanetworks/charts/ella-ims --version <version> --namespace ella -f values.yaml
        ```

    3. On the HSS and the PCRF, accept the new pod's Diameter host. Add its SIP address to the P-CSCF addresses of the core network.
    4. On the leader's **Cluster** page, verify the new node appears and is shown as **Healthy**.

## Remove a node

=== "Snap"

    1. Open the leader's **Cluster** page.
    2. In the node's **Node actions** menu, click **Drain this node**. Wait until its **Drain** column shows **Drained**.
    3. In the same menu, click **Remove from cluster**. For a node that is down for good, check **Force remove (skip drain)**.
    4. Remove the node from the HSS, the PCRF and the P-CSCF addresses of the core network.

=== "Kubernetes"

    1. Open the leader's **Cluster** page.
    2. In the **Node actions** menu of the node with the highest pod number, click **Drain this node**. Wait until its **Drain** column shows **Drained**.
    3. In the same menu, click **Remove from cluster**.
    4. In `values.yaml`, set `replicas` to `3`, upgrade the release, and delete the pod's volume:

        ```shell
        helm upgrade ella-ims oci://ghcr.io/ellanetworks/charts/ella-ims --version <version> --namespace ella -f values.yaml
        kubectl delete pvc data-ella-ims-3 -n ella
        ```

    5. Remove the node from the HSS, the PCRF and the P-CSCF addresses of the core network.
