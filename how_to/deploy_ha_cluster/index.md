# Deploy a High Availability Cluster

Ella IMS can be deployed as a high-availability cluster to provide redundancy and failover capabilities. See [High Availability](https://docs.ellanetworks.com/ims/explanation/high_availability/index.md) for more information.

## Prerequisites

- Three hosts meeting the standard [system requirements](https://docs.ellanetworks.com/ims/reference/system_reqs/index.md).
- Ella IMS installed on each host via the [Install](https://docs.ellanetworks.com/ims/how_to/install/index.md) guide, with `cluster.address` set in the configuration file.

## 1. Start node 1

Start node 1, open a browser at the API address and initialize it normally.

## 2. Create a join token for node 2

Still in node 1, open the **Cluster** page, click **Add Node**, click **Create Token**, then copy the token.

## 3. Start node 2

Start node 2, open a browser at the API address and click **Join an existing cluster instead**. Paste the token and click **Join**.

## 4. Add node 3

Repeat steps 2 and 3 for node 3.

## 5. Connect the network

- On the HSS and the PCRF, accept every node's Diameter host, shown on the **Cluster** page.
- In the core network, set the P-CSCF addresses to every node's SIP address.
- On each carrier, add every node's IBCF address.

## 6. Verify

On the leader's **Cluster** page, all three nodes appear as **Healthy**.

## Prerequisites

- A Kubernetes cluster with three nodes meeting the [Kubernetes requirements](https://docs.ellanetworks.com/ims/reference/system_reqs/#kubernetes).
- [Multus](https://github.com/k8snetworkplumbingwg/multus-cni), with a CNI plugin and IPAM that give each pod its own address on the network phones reach Ella IMS on.

## 1. Deploy

Create the namespace and the cluster secret:

```
kubectl create namespace ella
kubectl label namespace ella pod-security.kubernetes.io/enforce=privileged pod-security.kubernetes.io/warn=privileged
kubectl create secret generic ella-ims-cluster -n ella --from-literal=secret="$(openssl rand -base64 32)"
```

In namespace `ella`, create a NetworkAttachmentDefinition named `ims-sip` with that CNI plugin and IPAM.

Create `values.yaml`:

```
replicas: 3
sip:
  address: ""
  networkAttachment:
    create: false
    name: ims-sip
cluster:
  secretName: ella-ims-cluster
```

Deploy Ella IMS:

```
helm install ella-ims oci://ghcr.io/ellanetworks/charts/ella-ims --namespace ella -f values.yaml
```

## 2. Initialize

```
kubectl port-forward -n ella pod/ella-ims-0 5020:5020
```

Open `http://localhost:5020` and initialize it normally.

## 3. Connect the network

- On the HSS and the PCRF, accept every pod's Diameter host, shown on the **Cluster** page.
- In the core network, set the P-CSCF addresses to every pod's SIP address.

## 4. Verify

On the leader's **Cluster** page, all three nodes appear as **Healthy**.
