---
description: Step-by-step instructions to install Ella IMS.
---

# Install

Choose one of the installation methods below.

=== "Snap (Recommended)"

    Install the Ella IMS snap:

    ```shell
    sudo snap install ella-ims
    sudo snap connect ella-ims:network-control
    ```

    Configure Ella IMS:

    ```shell
    sudo vim /var/snap/ella-ims/common/ims.yaml
    ```

    Start Ella IMS:

    ```shell
    sudo snap start --enable ella-ims.imsd
    ```

=== "Source"

    Install the required dependencies:

    ```shell
    sudo snap install go --channel=1.27/stable --classic
    sudo snap install node --channel=24/stable --classic
    sudo apt update
    sudo apt -y install gcc
    ```

    Clone the Ella IMS repository:

    ```shell
    git clone https://github.com/ellanetworks/ims.git
    cd ims
    ```

    Build the frontend:

    ```shell
    npm install --prefix ui
    npm run build --prefix ui
    ```

    Build Ella IMS:

    ```shell
    go build -o ims -ldflags "-s -w -X github.com/ellanetworks/ims/version.GitCommit=$(git rev-parse HEAD)" ./cmd/ims
    ```

    Configure Ella IMS:

    ```shell
    vim ims.yaml
    ```

    Start Ella IMS:

    ```shell
    sudo ./ims --config ims.yaml
    ```

=== "Docker"

    Create a new directory:

    ```shell
    mkdir ella-ims
    cd ella-ims
    ```

    Copy the following file into this directory:

    ```yaml title="docker-compose.yaml"
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

    ```shell
    docker compose up -d
    ```

=== "Kubernetes"

    Ensure your Kubernetes cluster is running with the [Multus CNI](https://github.com/k8snetworkplumbingwg/multus-cni) installed.

    Download the Ella IMS manifests:

    ```shell
    git clone --depth 1 https://github.com/ellanetworks/ims.git
    ```

    Edit `ims/k8s/ims-sip-nad.yaml`: set `master`, the SIP address, and the route to the phones' IP pool through the core's N6 (5G) or SGi (4G) address.

    Edit `ims/k8s/ims-diameter-nad.yaml`: set `master` and the Diameter address.

    Edit `ims/k8s/ims-configmap.yaml`: set `sip.addresses` and `diameter.address` to the same addresses.

    Create the namespace:

    ```shell
    kubectl create namespace ella
    kubectl label namespace ella pod-security.kubernetes.io/enforce=privileged
    ```

    Deploy Ella IMS:

    ```shell
    kubectl apply -k ims/k8s
    ```

    Ella IMS's UI and API are on the `ella-ims` NodePort Service.

Ella IMS's UI and API are at `http://<host-ip>:5020`. Open the UI and create the first user.

See the [configuration reference](../reference/config_file.md) for all options.
