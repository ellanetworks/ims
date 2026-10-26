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

See the [configuration reference](../reference/config_file.md) for all options.
