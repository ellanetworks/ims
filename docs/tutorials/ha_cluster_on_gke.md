---
description: A hands-on tutorial to run Ella IMS as a high-availability cluster on Google Kubernetes Engine.
---

# Run a High Availability Cluster on GKE

In this tutorial, you run Ella IMS as a three-node cluster on Google Kubernetes Engine (GKE), and watch it survive the loss of a node and a rolling restart.

## Pre-requisites

- A Google Cloud project with billing and the Kubernetes Engine API enabled.
- The `gcloud` CLI with the `gke-gcloud-auth-plugin` component, logged in to that project.
- `kubectl` and `git`.

## 1. Create a GKE cluster

```shell
gcloud container clusters create ella-ims \
  --zone us-central1-a \
  --machine-type e2-standard-2 \
  --num-nodes 1 \
  --enable-dataplane-v2 \
  --enable-ip-alias \
  --enable-multi-networking
gcloud container clusters get-credentials ella-ims --zone us-central1-a
```

## 2. Create the SIP network

```shell
gcloud compute networks create ims-sip --subnet-mode custom
gcloud compute networks subnets create ims-sip \
  --network ims-sip \
  --region us-central1 \
  --range 10.60.0.0/24
gcloud compute firewall-rules create ims-sip-allow-internal \
  --network ims-sip \
  --allow esp,udp,tcp,icmp \
  --source-ranges 10.60.0.0/24
```

## 3. Create the SIP node pool

Create three nodes with a second NIC on the SIP subnet. Each Ella IMS node runs on one of them, with that NIC as `eth1`:

```shell
gcloud container node-pools create ims-sip \
  --cluster ella-ims \
  --zone us-central1-a \
  --machine-type e2-standard-2 \
  --num-nodes 3 \
  --image-type UBUNTU_CONTAINERD \
  --additional-node-network network=ims-sip,subnetwork=ims-sip
```

## 4. Deploy Ella IMS

```shell
git clone --depth 1 https://github.com/ellanetworks/ims.git
kubectl create namespace ella
kubectl label namespace ella pod-security.kubernetes.io/enforce=privileged
kubectl create secret generic ella-ims-cluster -n ella --from-literal=secret="$(openssl rand -base64 32)"
kubectl apply -k ims/k8s/examples/gke-ha
kubectl get pods -n ella --watch
```

Wait until the three pods are `Running`, then press `Ctrl+C`.

## 5. Initialize Ella IMS

```shell
kubectl port-forward -n ella pod/ella-ims-0 5020:5020
```

Open `http://localhost:5020` and create the first user.

## 6. Look at the cluster

Open the **Cluster** page. It names the leader. Only the leader shows the health of the nodes: if `ella-ims-0` is not the leader, stop the port-forward with `Ctrl+C`, and start it again on the leader's pod:

```shell
kubectl port-forward -n ella pod/ella-ims-<n> 5020:5020
```

On the leader's **Cluster** page, the three nodes appear as **Healthy**, each with its own **Diameter Host**.

## 7. Lose a node

In another terminal, delete the leader's pod:

```shell
kubectl delete pod -n ella ella-ims-<n>
```

The port-forward stops with the pod. Start it again on another pod, and open the **Cluster** page: another node is the leader. Open the new leader's page as in step 6. The deleted pod comes back, and its node returns to **Healthy** with the same **Diameter Host**.

## 8. Restart the nodes one at a time

Stop the port-forward, then:

```shell
kubectl rollout restart statefulset/ella-ims -n ella
kubectl rollout status statefulset/ella-ims -n ella
```

Open the leader's **Cluster** page as in step 6: all three nodes are **Healthy**.

## 9. Clean up

```shell
kubectl delete statefulset ella-ims -n ella --wait
kubectl wait --for=delete pod -l app.kubernetes.io/name=ella-ims -n ella --timeout=2m
kubectl delete -k ims/k8s/examples/gke-ha --ignore-not-found
kubectl delete pvc -n ella --all
kubectl wait --for=delete pv --all --timeout=5m
gcloud container clusters delete ella-ims --zone us-central1-a
gcloud compute firewall-rules delete ims-sip-allow-internal
gcloud compute networks subnets delete ims-sip --region us-central1
gcloud compute networks delete ims-sip
```
