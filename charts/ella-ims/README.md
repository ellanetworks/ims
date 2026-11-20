# Ella IMS Helm Chart

[Ella IMS](https://docs.ellanetworks.com/ims/) is an IP Multimedia Subsystem for private 4G and 5G networks. This chart deploys Ella IMS as a standalone node or as a high-availability cluster.

## Prerequisites

The cluster must meet the [Kubernetes requirements](https://docs.ellanetworks.com/ims/reference/system_reqs/#kubernetes).

## Install

```bash
kubectl create namespace ella
kubectl label namespace ella pod-security.kubernetes.io/enforce=privileged pod-security.kubernetes.io/warn=privileged
helm install ella-ims oci://ghcr.io/ellanetworks/charts/ella-ims --namespace ella -f values.yaml
```

A standalone node, with its SIP interface on a macvlan network:

```yaml
sip:
  address: 10.6.0.5/24
  networkAttachment:
    master: eth1
    routes:
      - dst: 10.46.0.0/16
        gw: 10.6.0.2
```

A high-availability cluster, with your own NetworkAttachmentDefinition that gives each pod its own SIP address, and a Secret with the cluster secret:

```bash
kubectl create secret generic ella-ims-cluster -n ella --from-literal=secret="$(openssl rand -base64 32)"
```

```yaml
replicas: 3
sip:
  address: ""
  networkAttachment:
    create: false
    name: ims-sip
cluster:
  secretName: ella-ims-cluster
```

See [Install](https://docs.ellanetworks.com/ims/how_to/install/) and [Deploy a High Availability Cluster](https://docs.ellanetworks.com/ims/how_to/deploy_ha_cluster/).

## Examples

A SIP interface that the cluster's network provides, here GKE multi-network:

```yaml
sip:
  address: ""
  networkAttachment:
    create: false
nodeSelector:
  cloud.google.com/gke-nodepool: ims-sip
podAnnotations:
  networking.gke.io/default-interface: eth0
  networking.gke.io/interfaces: '[{"interfaceName":"eth0","network":"default"},{"interfaceName":"eth1","network":"ims-sip"}]'
```

A fixed SIP address, such as a load balancer's, that an init container adds to a SIP interface that the cluster's network provides:

```yaml
sip:
  address: ""
  networkAttachment:
    create: false
config:
  sip:
    addresses: [10.60.0.100]
extraInitContainers:
  - name: sip-address
    image: busybox:1.36
    command: ["sh", "-c", "ip addr add 10.60.0.100/32 dev eth1 || true"]
    securityContext:
      runAsUser: 0
      allowPrivilegeEscalation: false
      readOnlyRootFilesystem: true
      capabilities:
        drop: ["ALL"]
        add: ["NET_ADMIN"]
```

## Values

| Key | Default | Description |
|-----|---------|-------------|
| `replicas` | `1` | Number of Ella IMS pods. `1` deploys a standalone node. `3` or more deploy a high-availability cluster, with one pod per Kubernetes node. |
| `sip.interface` | `eth1` | Name of the SIP interface in the pod. The CSCFs listen on its addresses. |
| `sip.address` | `10.6.0.5/24` | SIP address of the pod, in CIDR notation, set through the Multus network annotation. Empty for none. Must be empty with more than one replica. |
| `sip.networkAttachment.create` | `true` | Create a macvlan NetworkAttachmentDefinition with static IPAM. Set to `false` to use your own. |
| `sip.networkAttachment.name` | `<release>-sip` | Name of the NetworkAttachmentDefinition. With `create` `false` and an empty name, the pod gets no Multus network. |
| `sip.networkAttachment.master` | `eth1` | Host interface on the network that phones reach Ella IMS on. |
| `sip.networkAttachment.mode` | `bridge` | macvlan mode. |
| `sip.networkAttachment.routes` | `[]` | Routes through the SIP interface, e.g. `{dst: 10.46.0.0/16, gw: 10.6.0.2}` for the phones' IP pool through the core's N6 or SGi address. |
| `cluster.secretName` | | Name of the Secret that holds the cluster secret in its `secret` key. Required with more than one replica. |
| `config` | `logging.level: info` | [Configuration file](https://docs.ellanetworks.com/ims/reference/config_file/) content, merged over the database, API, SIP, Diameter and cluster settings that the chart sets. |
| `image.repository` | `ghcr.io/ellanetworks/ella-ims` | Image repository. |
| `image.tag` | chart `appVersion` | Image tag. |
| `image.digest` | | Image digest. Takes precedence over `image.tag`. |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy. |
| `imagePullSecrets` | `[]` | Image pull secrets. |
| `service.type` | `ClusterIP` | Type of the API service. |
| `service.port` | `5020` | Port of the API service. |
| `service.nodePort` | | Node port, when `service.type` is `NodePort`. |
| `service.annotations` | `{}` | Annotations of the API service. |
| `ingress.enabled` | `false` | Create an Ingress that exposes the API and UI. |
| `ingress.className` | | IngressClass name. Empty uses the cluster default. |
| `ingress.host` | | Host name of the API and UI. Required when the Ingress is enabled. |
| `ingress.annotations` | `{}` | Ingress annotations, e.g. for cert-manager. |
| `ingress.tls` | `[]` | Ingress TLS entries, each with `secretName` and `hosts`. |
| `networkPolicy.enabled` | `true` | Create a NetworkPolicy that limits the cluster port to the Ella IMS pods, with more than one replica. |
| `persistence.size` | `10Gi` | Size of each pod's data volume. |
| `persistence.storageClassName` | default StorageClass | StorageClass of the data volumes. |
| `resources` | 500m CPU, 256 MiB requested; 16 CPU, 1 GiB limit | Container resources. |
| `clusterDomain` | `cluster.local` | Kubernetes cluster domain, used in the cluster addresses of high-availability pods. |
| `nodeSelector` | `{}` | Node selector. |
| `tolerations` | `[]` | Tolerations. |
| `affinity` | one pod per Kubernetes node in high availability | Affinity. Replaces the default anti-affinity. |
| `priorityClassName` | | Priority class. |
| `serviceAccount.create` | `true` | Create a service account. |
| `serviceAccount.name` | `<release>` | Service account name. |
| `serviceAccount.annotations` | `{}` | Service account annotations. |
| `serviceMonitor.enabled` | `false` | Create a Prometheus Operator ServiceMonitor for `/api/v1/metrics`. |
| `serviceMonitor.interval` | `30s` | Scrape interval. |
| `serviceMonitor.labels` | `{}` | Extra ServiceMonitor labels. |
| `podAnnotations` | `{}` | Extra pod annotations. |
| `podLabels` | `{}` | Extra pod labels. |
| `extraInitContainers` | `[]` | Init containers that run before Ella IMS, such as one that sets the SIP address. |
| `extraVolumes` | `[]` | Extra pod volumes, for `extraInitContainers`. |
| `nameOverride` | | Overrides the chart name. |
| `fullnameOverride` | | Overrides the resource name prefix. |
| `tests.image.repository` | `registry.access.redhat.com/ubi10/ubi-minimal` | Image of the `helm test` pod. |
| `tests.image.tag` | `10.2` | Tag of the `helm test` image. |
