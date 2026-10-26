---
description: Step-by-step instructions to connect Ella IMS to Ella Core.
---

# Integrate with Ella Core

This guide connects Ella IMS to Ella Core, with Ella Core as the HSS and the PCRF.

## Prerequisites

- **Ella IMS**: installed and started, as in [Install](install.md).

The examples use these addresses:

| Address | Role |
|---|---|
| `10.3.0.2` | Ella Core's Diameter address |
| `10.6.0.2` | Ella Core's N6 address |
| `10.3.0.5` | Ella IMS's Diameter address |
| `10.6.0.5` | Ella IMS's SIP address |
| `10.46.0.0/16` | IP pool of Ella Core's `ims` data network |

## 1. Enable Diameter in Ella Core

In Ella Core's configuration file, set the Diameter address, and restart Ella Core:

```yaml
interfaces:
  diameter:
    address: "10.3.0.2"
```

## 2. Set the Ella IMS addresses

In `ims.yaml`, set the address phones reach Ella IMS on, and the address it reaches Ella Core from, and restart Ella IMS:

```yaml
sip:
  addresses: [10.6.0.5]
diameter:
  address: 10.3.0.5
```

## 3. Set the operator

In the Ella IMS UI, go to the **Operator** page:

- Click the edit icon next to **Operator ID (MCC/MNC)**, enter Ella Core's MCC and MNC, e.g. `001` / `01`, and click **Update**.
- Click the edit icon next to **Numbering Plan**, enter your country code and prefixes, and click **Update**.

## 4. Add Ella Core as the HSS and PCRF

Go to the **Cores** page:

- Under **Peers**, click **Add Peer**, enter `10.3.0.2`, check **HSS** and **PCRF**, and click **Add**.
- Under **HSS**, click the edit icon next to **HSS Realm**, enter `epc.mnc<MNC>.mcc<MCC>.3gppnetwork.org`, with the MNC padded to 3 digits, e.g. `epc.mnc001.mcc001.3gppnetwork.org`, and click **Update**.
- Under **Voice QoS**, click the edit icon next to **Policy Function**, select **PCRF**, and click **Update**.
- Click the edit icon next to **PCRF Realm**, enter the same realm, and click **Update**.

## 5. Route the phones' traffic

On the Ella IMS host, route the `ims` data network through Ella Core's N6 address:

```shell
sudo ip route add 10.46.0.0/16 via 10.6.0.2 mtu lock 1400
```

## 6. Point Ella Core to Ella IMS

In Ella Core, set **Operator → IMS → P-CSCF Addresses** to `10.6.0.5`, then follow Ella Core's [Enable Voice](https://docs.ellanetworks.com/how_to/enable_voice/) guide.
