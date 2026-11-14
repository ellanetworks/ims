# Install

Choose one of the installation methods below.

Install the Ella IMS snap:

```
sudo snap install ella-ims
sudo snap connect ella-ims:network-control
```

Configure Ella IMS:

```
sudo vim /var/snap/ella-ims/common/ims.yaml
```

Start Ella IMS:

```
sudo snap start --enable ella-ims.imsd
```

Ella IMS's UI and API are at `http://<host-ip>:5020`.

Install the required dependencies:

```
sudo snap install go --channel=1.27/stable --classic
sudo snap install node --channel=24/stable --classic
sudo apt update
sudo apt -y install gcc
```

Clone the Ella IMS repository:

```
git clone https://github.com/ellanetworks/ims.git
cd ims
```

Build the frontend:

```
npm install --prefix ui
npm run build --prefix ui
```

Build Ella IMS:

```
go build -o ims -ldflags "-s -w -X github.com/ellanetworks/ims/version.GitCommit=$(git rev-parse HEAD)" ./cmd/ims
```

Configure Ella IMS:

```
vim ims.yaml
```

Start Ella IMS:

```
sudo ./ims --config ims.yaml
```

Ella IMS's UI and API are at `http://<host-ip>:5020`.

Create a new directory:

```
mkdir ella-ims
cd ella-ims
```

Copy the following file into this directory:

docker-compose.yaml

```
configs:
  ella_ims_config:
    content: |
      logging:
        level: info
      db:
        path: /data/ims.db
      api:
        address: 0.0.0.0
        port: 5020
      sip:
        addresses: [10.6.0.5]
      diameter:
        address: 10.3.0.5

services:
  ella-ims:
    image: ghcr.io/ellanetworks/ella-ims:latest
    configs:
      - source: ella_ims_config
        target: /ims.yaml
    entrypoint: /bin/ims --config /ims.yaml
    restart: unless-stopped
    stop_grace_period: 37s
    network_mode: host
    cap_add:
      - NET_ADMIN
    volumes:
      - ella-ims-data:/data

volumes:
  ella-ims-data:
```

Set `sip.addresses` and `diameter.address` to addresses of the host.

Start the Ella IMS container:

```
docker compose up -d
```

Ella IMS's UI and API are at `http://<host-ip>:5020`.

Ensure your Kubernetes cluster nodes meet the [system requirements](https://docs.ellanetworks.com/ims/reference/system_reqs/index.md).

Download the Ella IMS manifests:

```
git clone --depth 1 https://github.com/ellanetworks/ims.git
```

Create the namespace with the privileged Pod Security Standard:

```
kubectl create namespace ella
kubectl label namespace ella pod-security.kubernetes.io/enforce=privileged
```

Choose an example:

| Example                          | Second interface, `eth1`                                                                                                                                          | Files to edit                                                                                                                                                                                                                                                                                  |
| -------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ims/k8s/examples/multus`        | [Multus](https://github.com/k8snetworkplumbingwg/multus-cni), with the `macvlan` and `static` CNI plugins on the nodes                                            | `ims-sip-nad.yaml`: set `master` to the node's interface on the network phones reach Ella IMS on, the P-CSCF address, and the route to the phones' IP pool through the core's N6 (5G) or SGi (4G) address.                                                                                     |
| `ims/k8s/examples/gke-netdevice` | [GKE multi-network](https://cloud.google.com/kubernetes-engine/docs/how-to/setup-multinetwork-support-for-pods) and an internal passthrough Network Load Balancer | `ims-sip-network.yaml`: set `vpc`, `vpcSubnet`, and the phones' IP pool in `routes`. Keep the routes for Google Cloud's health check ranges. `ims.yaml`: set `sip.addresses` to the load balancer's address. `kustomization.yaml`: set the same address in the `pcscf-address` init container. |

Give the pod its second interface, `eth1`, on the network phones reach Ella IMS on, as the example requires.

Edit the example's addresses in its files.

Deploy Ella IMS:

```
kubectl apply -k ims/k8s/examples/<example>
```

Find the P-CSCF address, the `pcscf` entry of `sip`:

```
kubectl logs -n ella ella-ims-0 | grep '"ims started"'
```

Open the UI and API on your machine:

```
kubectl port-forward -n ella svc/ella-ims 5020:5020
```

Ella IMS's UI and API are at `http://localhost:5020`.

On the HSS and the PCRF, set Ella IMS's Diameter host to `ella-ims-0.ims.mnc<MNC>.mcc<MCC>.3gppnetwork.org`, and accept its connections from the pod's address, or from the node's where the cluster masquerades pod traffic.

Open the UI and create the first user.

See the [configuration reference](https://docs.ellanetworks.com/ims/reference/config_file/index.md) for all options.
