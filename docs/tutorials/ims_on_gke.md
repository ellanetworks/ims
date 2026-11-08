---
description: A hands-on tutorial to run Ella IMS on Google Kubernetes Engine, with the P-CSCF on its own network and a fixed address.
---

# Run Ella IMS on GKE

In this tutorial, you run Ella IMS on Google Kubernetes Engine (GKE), with the P-CSCF on a second interface from GKE multi-network, at the fixed address `10.60.0.100` of an internal passthrough load balancer.

You can expect to spend about 30 minutes completing this tutorial.

## Pre-requisites

- A Google Cloud project with billing and the Kubernetes Engine API enabled.
- The `gcloud` CLI with the `gke-gcloud-auth-plugin` component, logged in to that project.
- `kubectl` and `git`.

## 1. Create a GKE cluster

Create a cluster with Dataplane V2 and multi-networking:

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

Create a VPC and a subnet for SIP, and allow IPsec (ESP), SIP and ping on it:

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

Create a node pool whose nodes have a second NIC on the SIP subnet. GKE passes that NIC to the Ella IMS pod as `eth1`:

```shell
gcloud container node-pools create ims-sip \
  --cluster ella-ims \
  --zone us-central1-a \
  --machine-type e2-standard-2 \
  --num-nodes 1 \
  --image-type UBUNTU_CONTAINERD \
  --additional-node-network network=ims-sip,subnetwork=ims-sip
```

## 4. Give the P-CSCF a fixed address

Reserve `10.60.0.100` on the SIP subnet, and send all its traffic to the node pool through an internal passthrough Network Load Balancer:

```shell
gcloud compute addresses create ims-sip-vip \
  --region us-central1 \
  --subnet ims-sip \
  --addresses 10.60.0.100
gcloud compute health-checks create tcp ims-sip-hc \
  --region us-central1 \
  --port 5060
gcloud compute firewall-rules create ims-sip-allow-hc \
  --network ims-sip \
  --allow tcp:5060 \
  --source-ranges 35.191.0.0/16,130.211.0.0/22
gcloud compute backend-services create ims-sip-bs \
  --region us-central1 \
  --load-balancing-scheme INTERNAL \
  --protocol UNSPECIFIED \
  --health-checks ims-sip-hc \
  --health-checks-region us-central1 \
  --network ims-sip
gcloud compute backend-services add-backend ims-sip-bs \
  --region us-central1 \
  --instance-group "$(gcloud container node-pools describe ims-sip --cluster ella-ims --zone us-central1-a --format 'value(instanceGroupUrls[0].basename())')" \
  --instance-group-zone us-central1-a
gcloud compute forwarding-rules create ims-sip-fr \
  --region us-central1 \
  --load-balancing-scheme INTERNAL \
  --ip-protocol L3_DEFAULT \
  --ports ALL \
  --network ims-sip \
  --subnet ims-sip \
  --address ims-sip-vip \
  --backend-service ims-sip-bs
```

## 5. Deploy Ella IMS

```shell
git clone --depth 1 https://github.com/ellanetworks/ims.git
kubectl create namespace ella
kubectl label namespace ella pod-security.kubernetes.io/enforce=privileged
kubectl apply -k ims/k8s/examples/gke-netdevice
kubectl rollout status statefulset/ella-ims -n ella
```

## 6. Check the P-CSCF address

```shell
kubectl logs -n ella ella-ims-0 | grep '"ims started"'
```

The `sip` entries show `pcscf 10.60.0.100:5060`.

```shell
gcloud compute backend-services get-health ims-sip-bs --region us-central1
```

The node running Ella IMS shows as `HEALTHY`.

## 7. Initialize Ella IMS

```shell
kubectl port-forward -n ella svc/ella-ims 5020:5020
```

Open `http://localhost:5020`. The **Initialize Ella IMS** page opens. Enter an email and a password of at least 8 characters, and click **Create**. Then stop the port-forward with `Ctrl+C`.

!!! success

    Ella IMS runs on GKE, with the P-CSCF at `10.60.0.100`.

To connect phones, route the phones' IP pool in the SIP VPC to your core's N6 (5G) or SGi (4G) address, without NAT. Set the core's P-CSCF address to `10.60.0.100`, and follow [Integrate with Ella Core](../how_to/integrate_with_ella_core.md).

## 8. Clean up

```shell
kubectl delete statefulset ella-ims -n ella --wait
kubectl wait --for=delete pod/ella-ims-0 -n ella --timeout=2m
kubectl delete -k ims/k8s/examples/gke-netdevice
kubectl delete pvc -n ella --all
kubectl wait --for=delete pv --all --timeout=5m
gcloud compute forwarding-rules delete ims-sip-fr --region us-central1
gcloud compute backend-services delete ims-sip-bs --region us-central1
gcloud compute health-checks delete ims-sip-hc --region us-central1
gcloud compute addresses delete ims-sip-vip --region us-central1
gcloud container clusters delete ella-ims --zone us-central1-a
gcloud compute firewall-rules delete ims-sip-allow-internal ims-sip-allow-hc
gcloud compute networks subnets delete ims-sip --region us-central1
gcloud compute networks delete ims-sip
```
