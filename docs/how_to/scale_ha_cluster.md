---
description: Add and remove nodes in a running Ella IMS high-availability cluster.
---

# Scale a High Availability Cluster

This guide adds a node to a running Ella IMS cluster, and removes one.

## Prerequisites

- A running HA cluster deployed (see [Deploy a High Availability Cluster](deploy_ha_cluster.md)).
- A prepared host with Ella IMS installed per the [Install](install.md) guide with `cluster.address` set in the configuration file.

## Add a node

1. Open the **Cluster** page on any node in the cluster.
2. Click **Add Node**, click **Create Token**, and copy the token.
3. Start Ella IMS on the new host.
4. Open the new node's UI in a browser. Click **Join an existing cluster instead**, paste the token, and click **Join**.
5. Accept the new node's Diameter host on the HSS and the PCRF, and add its SIP address to the P-CSCF addresses of the core network.
6. On the **Cluster** page, verify the new node appears and is shown as **Healthy**.

## Remove a node

1. On the **Cluster** page of the leader, drain the node.
2. Once it is drained, remove it.
3. Stop Ella IMS on its host.
