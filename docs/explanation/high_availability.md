---
description: Explanation of how high availability works in Ella IMS.
---

# High Availability

High availability (HA) lets you run an Ella IMS cluster so that the IMS keeps working when nodes fail. Each node is active and serves phones.

HA is designed around the [Raft Consensus Algorithm](https://raft.github.io/): at any time one node is the leader, it is the only node that commits writes, and every write replicates to a majority of voters before it is considered committed. Nodes communicate together via mTLS to share changes.

## Failure tolerance

A cluster keeps accepting writes as long as a majority of its voters are alive. Deploy 3 or 5 nodes.

| Voters | Quorum | Failures tolerated |
| ------ | ------ | ------------------ |
| 3      | 2      | 1                  |
| 5      | 3      | 2                  |

Without a majority, phones keep registering and calling. Only changes to settings and users, and new MCPTT group calls, stop.

## What replicates, and what does not

Settings, users, SIP trunks, MCPTT groups and affiliations, call records and the cluster's members replicate across the cluster.

Registrations, IPsec security associations and calls do not. Each node keeps those of the phones it serves.

Per-node configuration (addresses, ports, Diameter host label) does not replicate. Configure it on every node.

Observability is per-node: the UI and API show the registrations, live calls and SIP trunk status of the node that answers, and each node exposes its own metrics.

## Failover

A phone registers through one node, which serves it. A call to that phone can arrive on any node, which forwards it to the phone's node.

When a node dies, its calls drop. Its phones register again through another node. This requires an HSS that supports IMS restoration.

## SIP trunks

Each node connects to the carrier on its own, so the carrier must accept every node's address.

## Draining a node

Draining prepares a node for removal: phones register through other nodes at their next re-registration, and the node hands over the leadership.

Drain is triggered by an operator via the cluster API or UI.

## Inter-node communication using mTLS

Every inter-node connection is mutually authenticated over TLS. Removing a node immediately revokes its access cluster-wide.

## Disaster recovery

If a majority of the voters are lost for good, restore the last backup on a new cluster. See [Back Up and Restore](../how_to/back_up_and_restore.md).

## Rolling upgrades

Upgrades proceed one node at a time. A restarted node keeps its registrations, so its phones stay registered. Downgrades are not supported.

## Further reading

- [Deploy a High Availability Cluster](../how_to/deploy_ha_cluster.md)
- [Scale a High Availability Cluster](../how_to/scale_ha_cluster.md)
- [Upgrade](../how_to/upgrade.md#upgrade-a-cluster)
- [Cluster API](../reference/api/cluster.md)
