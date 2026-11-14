# Run a High Availability Cluster in the Cloud

In this tutorial, you run Ella IMS as a three-node cluster on managed Kubernetes, and watch it survive the loss of a node and a rolling restart.

Note

This tutorial uses managed Kubernetes. Ella IMS can also run as a cluster on cloud virtual machines, installed as a snap, as in [Deploy a High Availability Cluster](https://docs.ellanetworks.com/ims/how_to/deploy_ha_cluster/index.md).

## Pre-requisites

- A Google Cloud project and the `gcloud` CLI with the `gke-gcloud-auth-plugin` component

- `kubectl` and `git`.

- An AWS account and the `aws` CLI

- `eksctl`, `kubectl` and `git`.

## 1. Create a Kubernetes cluster

```
gcloud container clusters create ella-ims \
  --zone us-central1-a \
  --machine-type e2-standard-2 \
  --num-nodes 1 \
  --enable-dataplane-v2 \
  --enable-ip-alias \
  --enable-multi-networking
gcloud container clusters get-credentials ella-ims --zone us-central1-a
```

Create `cluster.yaml`:

```
apiVersion: eksctl.io/v1alpha5
kind: ClusterConfig
metadata:
  name: ella-ims
  region: us-east-1
availabilityZones: [us-east-1a, us-east-1b]
addons:
  - name: vpc-cni
    configurationValues: '{"enableNetworkPolicy":"true"}'
  - name: eks-pod-identity-agent
  - name: aws-ebs-csi-driver
    useDefaultPodIdentityAssociations: true
    configurationValues: '{"defaultStorageClass":{"enabled":true}}'
managedNodeGroups:
  - name: ella-ims
    instanceType: t3.medium
    desiredCapacity: 3
    availabilityZones: [us-east-1a]
```

Create the cluster:

```
eksctl create cluster -f cluster.yaml
```

## 2. Create the SIP network

```
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

```
export AWS_REGION=us-east-1
VPC=$(aws eks describe-cluster --name ella-ims --query cluster.resourcesVpcConfig.vpcId --output text)
SUBNET=$(aws ec2 create-subnet --vpc-id $VPC \
  --availability-zone us-east-1a \
  --cidr-block 192.168.128.0/24 \
  --tag-specifications 'ResourceType=subnet,Tags=[{Key=Name,Value=ims-sip}]' \
  --query Subnet.SubnetId --output text)
SG=$(aws ec2 create-security-group --vpc-id $VPC \
  --group-name ims-sip \
  --description "Ella IMS SIP" \
  --query GroupId --output text)
aws ec2 authorize-security-group-ingress --group-id $SG --protocol all --cidr 192.168.128.0/24
```

## 3. Connect the nodes to the SIP network

Create three nodes with a second NIC on the SIP subnet. Each Ella IMS node runs on one of them, with that NIC as `eth1`:

```
gcloud container node-pools create ims-sip \
  --cluster ella-ims \
  --zone us-central1-a \
  --machine-type e2-standard-2 \
  --num-nodes 3 \
  --image-type UBUNTU_CONTAINERD \
  --additional-node-network network=ims-sip,subnetwork=ims-sip
```

Give each node a second NIC on the SIP subnet. Each Ella IMS node runs on one of them, with that NIC as `eth1`:

```
for i in $(aws ec2 describe-instances \
  --filters Name=tag:eks:cluster-name,Values=ella-ims Name=instance-state-name,Values=running \
  --query 'Reservations[].Instances[].InstanceId' --output text); do
  ENI=$(aws ec2 create-network-interface --subnet-id $SUBNET --groups $SG \
    --tag-specifications 'ResourceType=network-interface,Tags=[{Key=Name,Value=ims-sip},{Key=node.k8s.amazonaws.com/no_manage,Value=true}]' \
    --query NetworkInterface.NetworkInterfaceId --output text)
  aws ec2 attach-network-interface --network-interface-id $ENI --instance-id $i --device-index 2
done
```

Install Multus, which gives the NIC to the pod:

```
kubectl apply -f https://raw.githubusercontent.com/k8snetworkplumbingwg/multus-cni/v4.3.1/deployments/multus-daemonset-thick.yml
kubectl rollout status daemonset/kube-multus-ds -n kube-system
```

## 4. Deploy Ella IMS

```
git clone --depth 1 https://github.com/ellanetworks/ims.git
kubectl create namespace ella
kubectl label namespace ella pod-security.kubernetes.io/enforce=privileged
kubectl create secret generic ella-ims-cluster -n ella --from-literal=secret="$(openssl rand -base64 32)"
kubectl apply -k ims/k8s/examples/gke-ha
kubectl get pods -n ella --watch
```

```
git clone --depth 1 https://github.com/ellanetworks/ims.git
kubectl create namespace ella
kubectl label namespace ella pod-security.kubernetes.io/enforce=privileged
kubectl create secret generic ella-ims-cluster -n ella --from-literal=secret="$(openssl rand -base64 32)"
kubectl apply -k ims/k8s/examples/eks-ha
kubectl get pods -n ella --watch
```

Wait until the three pods are `Running`, then press `Ctrl+C`.

## 5. Initialize Ella IMS

```
kubectl port-forward -n ella pod/ella-ims-0 5020:5020
```

Open `http://localhost:5020` and create the first user.

## 6. Look at the cluster

Open the **Cluster** page. It names the leader. Only the leader shows the health of the nodes: if `ella-ims-0` is not the leader, stop the port-forward with `Ctrl+C`, and start it again on the leader's pod:

```
kubectl port-forward -n ella pod/ella-ims-<n> 5020:5020
```

On the leader's **Cluster** page, the three nodes appear as **Healthy**, each with its own **Diameter Host**.

## 7. Lose a node

In another terminal, delete the leader's pod:

```
kubectl delete pod -n ella ella-ims-<n>
```

The port-forward stops with the pod. Start it again on another pod, and open the **Cluster** page: another node is the leader. Open the new leader's page as in step 6. The deleted pod comes back, and its node returns to **Healthy** with the same **Diameter Host**.

## 8. Restart the nodes one at a time

Stop the port-forward, then:

```
kubectl rollout restart statefulset/ella-ims -n ella
kubectl rollout status statefulset/ella-ims -n ella
```

Open the leader's **Cluster** page as in step 6: all three nodes are **Healthy**.

## 9. Clean up

```
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

```
kubectl delete statefulset ella-ims -n ella --wait
kubectl wait --for=delete pod -l app.kubernetes.io/name=ella-ims -n ella --timeout=2m
kubectl delete -k ims/k8s/examples/eks-ha --ignore-not-found
kubectl delete pvc -n ella --all
kubectl wait --for=delete pv --all --timeout=5m
export AWS_REGION=us-east-1
for ENI in $(aws ec2 describe-network-interfaces --filters Name=tag:Name,Values=ims-sip \
  --query 'NetworkInterfaces[].NetworkInterfaceId' --output text); do
  aws ec2 detach-network-interface --attachment-id $(aws ec2 describe-network-interfaces \
    --network-interface-ids $ENI --query 'NetworkInterfaces[0].Attachment.AttachmentId' --output text)
  aws ec2 wait network-interface-available --network-interface-ids $ENI
  aws ec2 delete-network-interface --network-interface-id $ENI
done
aws ec2 delete-security-group --group-id $(aws ec2 describe-security-groups \
  --filters Name=group-name,Values=ims-sip --query 'SecurityGroups[0].GroupId' --output text)
aws ec2 delete-subnet --subnet-id $(aws ec2 describe-subnets \
  --filters Name=tag:Name,Values=ims-sip --query 'Subnets[0].SubnetId' --output text)
eksctl delete cluster -f cluster.yaml --wait
```
