# Scale a High Availability Cluster

This guide walks through adding a node to an existing Ella IMS high-availability cluster, and removing one.

## Prerequisites

- A running HA cluster deployed (see [Deploy a High Availability Cluster](https://docs.ellanetworks.com/ims/how_to/deploy_ha_cluster/index.md)).

## Add a node

1. Prepare a host with Ella IMS installed per the [Install](https://docs.ellanetworks.com/ims/how_to/install/index.md) guide, with a `cluster` block in the configuration file.

1. Open the **Cluster** page on any node in the cluster.

1. Click **Add Node**, click **Create Token**, and copy the token.

1. Start Ella IMS on the new host.

1. Open the new node's UI in a browser. Click **Join an existing cluster instead**, paste the token, and click **Join**.

1. On the HSS and the PCRF, accept the new node's Diameter host. Add its SIP address to the P-CSCF addresses of the core network.

1. On the leader's **Cluster** page, verify the new node appears and is shown as **Healthy**.

1. Add a Kubernetes node for the new pod, with a second interface, as for the others.

1. Add a pod:

   ```
   kubectl scale statefulset ella-ims -n ella --replicas=4
   ```

1. On the HSS and the PCRF, accept the new pod's Diameter host. Add its SIP address to the P-CSCF addresses of the core network.

1. On the leader's **Cluster** page, verify the new node appears and is shown as **Healthy**.

## Remove a node

1. Open the leader's **Cluster** page.

1. In the node's **Node actions** menu, click **Drain this node**. Wait until its **Drain** column shows **Drained**.

1. In the same menu, click **Remove from cluster**. For a node that is down for good, check **Force remove (skip drain)**.

1. Remove the node from the HSS, the PCRF and the P-CSCF addresses of the core network.

1. Open the leader's **Cluster** page.

1. In the **Node actions** menu of the node with the highest pod number, click **Drain this node**. Wait until its **Drain** column shows **Drained**.

1. In the same menu, click **Remove from cluster**.

1. Remove its pod and volume:

   ```
   kubectl scale statefulset ella-ims -n ella --replicas=3
   kubectl delete pvc data-ella-ims-3 -n ella
   ```

1. Remove the node from the HSS, the PCRF and the P-CSCF addresses of the core network.
