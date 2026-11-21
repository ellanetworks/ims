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

Ensure your Kubernetes cluster meets the [Kubernetes requirements](https://docs.ellanetworks.com/ims/reference/system_reqs/#kubernetes).

Create the namespace:

```
kubectl create namespace ella
kubectl label namespace ella pod-security.kubernetes.io/enforce=privileged pod-security.kubernetes.io/warn=privileged
```

Create `values.yaml`. Set `master` to the host interface on the network phones reach Ella IMS on, `address` to the P-CSCF address, and the route to the phones' IP pool through the core's N6 (5G) or SGi (4G) address:

```
sip:
  address: 10.6.0.5/24
  networkAttachment:
    master: eth1
    routes:
      - dst: 10.46.0.0/16
        gw: 10.6.0.2
```

Deploy Ella IMS:

```
helm install ella-ims oci://ghcr.io/ellanetworks/charts/ella-ims --namespace ella -f values.yaml
```

Open the UI and API on your machine:

```
kubectl port-forward -n ella service/ella-ims 5020:5020
```

Ella IMS's UI and API are at `http://localhost:5020`. See the [chart reference](https://github.com/ellanetworks/ims/tree/main/charts/ella-ims) for all values.

On the HSS and the PCRF, set Ella IMS's Diameter host to `ella-ims-0.ims.mnc<MNC>.mcc<MCC>.3gppnetwork.org`, and accept its connections from the pod's address, or from the node's where the cluster masquerades pod traffic.

Open the UI and create the first user.

See the [configuration reference](https://docs.ellanetworks.com/ims/reference/config_file/index.md) for all options.
