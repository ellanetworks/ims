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

    ```shell
    docker compose up -d
    ```

=== "Kubernetes"

    Ensure your Kubernetes cluster nodes meet the [system requirements](../reference/system_reqs.md).

    Phones reach the P-CSCF over IPsec, on its own address. Pod networks can drop or NAT IPsec, so the P-CSCF is always on a second interface of the pod, `eth1`, from [Multus](https://github.com/k8snetworkplumbingwg/multus-cni) or from [GKE multi-network](https://cloud.google.com/kubernetes-engine/docs/how-to/setup-multinetwork-support-for-pods). Diameter is on the pod network, `eth0`.

    Download the Ella IMS manifests:

    ```shell
    git clone --depth 1 https://github.com/ellanetworks/ims.git
    ```

    Create the namespace. IPsec needs the `NET_ADMIN` capability, as root, which only the privileged Pod Security Standard allows:

    ```shell
    kubectl create namespace ella
    kubectl label namespace ella pod-security.kubernetes.io/enforce=privileged
    ```

    === "Multus"

        Install Multus and the `macvlan` and `static` CNI plugins on the nodes.

        Edit `ims/k8s/examples/multus/ims-sip-nad.yaml`: set `master` to the node's interface on the network phones reach Ella IMS on, the P-CSCF address, and the route to the phones' IP pool through the core's N6 (5G) or SGi (4G) address.

        Deploy Ella IMS:

        ```shell
        kubectl apply -k ims/k8s/examples/multus
        ```

    === "GKE"

        Create a Standard cluster with `--enable-dataplane-v2`, `--enable-ip-alias` and `--enable-multi-networking`, and a VPC with a subnet for SIP that routes the phones' IP pool to the core's N6 (5G) or SGi (4G) address.

        Create a node pool with a second NIC on that subnet:

        ```shell
        gcloud container node-pools create ims-sip \
          --cluster <cluster> \
          --image-type cos_containerd \
          --additional-node-network network=<vpc>,subnetwork=<subnet>
        ```

        For Diameter over SCTP, use `--image-type UBUNTU_CONTAINERD` instead.

        The NIC's address changes when GKE recreates the node, and phones learn the P-CSCF address only when they set up their IMS PDU session (5G) or PDN connection (4G). Give the P-CSCF a fixed address: an internal passthrough Network Load Balancer on the SIP VPC, with a reserved address, an `L3_DEFAULT` forwarding rule on all ports, the node pool's instance group as backend, and a TCP health check on port 5060. [Run Ella IMS on GKE](../tutorials/ims_on_gke.md) has the commands.

        Edit `ims/k8s/examples/gke-netdevice/`:

        - `ims-sip-network.yaml`: set `vpc` and `vpcSubnet`, and the phones' IP pool in `routes`. Keep the routes for Google Cloud's health check ranges.
        - `ims.yaml`: set `sip.addresses` to the load balancer's address.
        - `kustomization.yaml`: set the same address in the `pcscf-address` init container.

        Deploy Ella IMS:

        ```shell
        kubectl apply -k ims/k8s/examples/gke-netdevice
        ```

        GKE passes the node's second NIC to the pod, so each node of the pool runs one Ella IMS pod. The P-CSCF address is the load balancer's, which needs one P-CSCF behind it: one load balancer per P-CSCF.

    Find the P-CSCF address, the `pcscf` entry of `sip`:

    ```shell
    kubectl logs -n ella ella-ims-0 | grep '"ims started"'
    ```

    Open the UI and API on your machine:

    ```shell
    kubectl port-forward -n ella svc/ella-ims 5020:5020
    ```

    Ella IMS's Diameter host is `ella-ims-0.ims.mnc<MNC>.mcc<MCC>.3gppnetwork.org`, after the pod's name. The HSS and the PCRF see its Diameter connections come from the pod's address, or from the node's where the cluster masquerades pod traffic.

Ella IMS's UI and API are at `http://<host-ip>:5020`. Open the UI and create the first user.

See the [configuration reference](../reference/config_file.md) for all options.
