---
description: Explanation of how high availability works in Ella IMS.
---

# High Availability

A high availability (HA) cluster is several Ella IMS nodes acting as one IMS core. Each node runs its own P-CSCF, I-CSCF and S-CSCF. Settings, users, MCPTT settings and call records are shared by every node. Registrations are not: each node keeps those of the phones it serves.

The cluster uses the [Raft](https://raft.github.io/) consensus algorithm for the shared data. A change is acknowledged only once a majority of the nodes have stored it, so it survives the loss of any minority of them. A single node runs the same way, as a cluster of one.

## Failure tolerance

The shared data can change as long as a majority of the voting nodes are up.

| Voters | Failures tolerated |
| ------ | ------------------ |
| 1      | 0                  |
| 3      | 1                  |
| 5      | 2                  |

Ella IMS keeps an odd number of voters. A cluster has 3 or 5 nodes, at most 5. A cluster is for availability, not for capacity: it serves as many subscribers as one node.

Phones keep registering and calling when a majority is lost. Only changes to the shared data, such as settings and users, stop.

## Shared and per-node data

Each node has two databases:

| Database | Holds | Replicated |
| -------- | ----- | ---------- |
| `ims.db` | Settings, users, API sessions and tokens, single sign-on, SIP trunks and routes, MCPTT settings, call records, and the cluster's members | Yes, through Raft |
| `node.db` | The S-CSCF's registrations and subscriptions, the P-CSCF's registrations and IPsec security associations, MCPTT registrations and affiliations, and the IP-SM-GW's registrations | No |

Per-node state stays out of Raft, so registering a phone never waits for the other nodes. The IPsec keys of a phone live in its node's kernel, and no other node could use them. Each node therefore needs its own network namespace: a node leaves alone the IPsec security associations of addresses that are not its own.

A backup holds `ims.db`. Restoring it keeps each node's `node.db`, so phones stay registered.

## Phones and their node

A phone registers through one node's P-CSCF, and that node's S-CSCF serves it. Each node's S-CSCF has its own name, `scscf-<label>.<domain>`, from its Diameter host label, so the HSS knows which node serves each phone.

A call to a phone can arrive on any node: from a SIP trunk, from another phone, or from an Application Server. The I-CSCF of that node asks the HSS which S-CSCF serves the phone, and forwards the call to that node. Each node learns the other nodes' S-CSCF addresses from the cluster's members, and trusts their SIP traffic, without DNS records or `sip.trusted_networks` entries for them.

## When a node is lost

The other nodes do not take over the lost node's registrations. Its phones move through IMS restoration (TS 23.380 clause 4):

- A phone that registers again goes through another node's P-CSCF. That node's I-CSCF cannot reach the lost S-CSCF, or gets a redirection or `480` from it, and selects its own node's S-CSCF instead.
- A call to a phone that has not registered again reaches a node whose I-CSCF cannot reach the lost S-CSCF. If the HSS supports IMS restoration, it lets that node's S-CSCF serve the phone, as an unregistered user until the phone registers again. Otherwise the call fails.

An HA cluster therefore requires an HSS that supports IMS restoration, so that calls reach the phones of a lost node before they register again.

A P-CSCF that does not answer the S-CSCF counts as lost, as one that has no binding for the phone does. If the HSS supports P-CSCF restoration, it then has the phone register again.

Calls through the lost node drop. When the node starts again, it closes its unfinished call records as incomplete.

A node that is lost for good stays a member until an operator removes it. Ella IMS never removes a member on its own: an unreachable node may only be restarting or cut off from the others, and a removed node never serves again until its data is deleted.

## Leader

One voter is the leader. Every change to the shared data goes through it: a node that receives a change forwards it to the leader. When the leader fails, the other nodes elect a new one within a few seconds. Until then, changes to the shared data fail, while phones keep registering and calling.

The leader learns that the other nodes are healthy from Raft's heartbeats, and puts a hash of its database in the log. Each node compares it with its own, so a node whose database diverged is detected and stops being ready.

Removing a member and restoring a backup run on the leader only. Another node answers `421`, with the leader's API address, instead of relaying the request.

## Diameter host

Each node has its own Diameter host, from `diameter.host_label`, and they share the realm. A node opens SIP and Diameter only once the cluster has recorded its label, so two nodes never present the same Diameter host to the HSS or the PCRF.

## Per-node views

The UI and the API show the registrations, live calls, SIP trunk status and MCPTT live state of the node that answers. Settings, users, call records and the cluster are the same on every node. The login rate limits apply per node.

`ellaims_registered_subscribers` counts the registrations of one node. Scrape every node.

## SIP trunks

Each node has its own IBCF, with its own external address. Configure a carrier with every node's address, or with one host name that resolves to all of them, as RFC 3263 expects. When a node is lost, the carrier sends new calls to the other nodes.

## Call records

Call records are shared, and each node writes those of the calls it handles: once when a call is answered, and once when it ends or fails. Ringing and media changes stay in memory until then, so a busy node adds about two writes per call to the shared log. A node closes its unfinished records as incomplete when it starts again, and removing a node closes its records. The leader alone deletes records past their retention.

## SMS over IP

Each node's IP-SM-GW registers its own phones at the HSS, which then routes SMS to that node. A node that restarts keeps these registrations, as it keeps its phones. A node that is drained or removed releases them, so SMS go back to other routes until the phones register through another node.

## Draining

Draining a node prepares it for removal: its P-CSCF answers `REGISTER` with `305 Use Proxy`, so phones register through another node at their next re-registration, and it hands the leadership over. It keeps its calls and its vote.

## Upgrades

Upgrade one node at a time. A restarted node keeps its registrations and IPsec security associations, so its phones stay registered, but calls through it drop. The cluster moves to the new version's database once every node runs it. Downgrades are not supported.

## Disaster recovery

If a majority of the voters are lost for good, the shared data can no longer change. Restore the last backup on a new cluster. See [Back Up and Restore](../how_to/back_up_and_restore.md).

## Further reading

- [Deploy a High Availability Cluster](../how_to/deploy_ha_cluster.md)
- [Scale a High Availability Cluster](../how_to/scale_ha_cluster.md)
- [Upgrade](../how_to/upgrade.md#upgrade-a-cluster)
- [Cluster API](../reference/api/cluster.md)
