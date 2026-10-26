---
description: Add Ella IMS to a private 5G network and make a voice call between two phones.
---

# Make Your First Voice Call

In this tutorial, we will add Ella IMS to a private 5G network running Ella Core, and make a voice call (VoNR) between two phones.

You can expect to spend about 30 minutes completing this tutorial.

## Pre-requisites

To complete this tutorial, you will need the following:

**Ella Core**

- A 5G network running Ella Core, as built in Ella Core's [Getting Started (5G Network with Radio Hardware)](https://docs.ellanetworks.com/tutorials/getting_started_hardware/) tutorial. In this tutorial, Ella Core's N6 interface, `ens3`, has the address `10.6.0.2`.

**Ella IMS**

- A second Linux machine running Ubuntu 26.04 LTS, on Ella Core's N6 network. In this tutorial, it has the address `10.6.0.5`.

**Phones**

- Two [5G phones that support VoNR](../reference/supported_mobile_equipment.md#phones), each with a SIM provisioned as a subscriber in Ella Core.

## 1. Enable Diameter in Ella Core

On the Ella Core machine, edit the configuration file:

```shell
sudo vim /var/snap/ella-core/common/core.yaml
```

Under `interfaces`, add the address Ella Core accepts Diameter connections on:

```yaml title="/var/snap/ella-core/common/core.yaml"
interfaces:
  diameter:
    address: "10.6.0.2"
```

Restart Ella Core:

```shell
sudo snap restart ella-core.cored
```

## 2. Install Ella IMS

On the Ella IMS machine, install the Ella IMS snap:

```shell
sudo snap install ella-ims
sudo snap connect ella-ims:network-control
```

Edit the configuration file:

```shell
sudo vim /var/snap/ella-ims/common/ims.yaml
```

Set the address phones reach Ella IMS on, and the address it reaches Ella Core from:

```yaml title="/var/snap/ella-ims/common/ims.yaml"
sip:
  addresses: [10.6.0.5]
diameter:
  address: 10.6.0.5
```

Start Ella IMS:

```shell
sudo snap start --enable ella-ims.imsd
```

Open your browser and navigate to `http://10.6.0.5:5020/`. You should see Ella IMS's **Cores** page.

## 3. Connect Ella IMS to Ella Core

Go to the **Cores** page:

- Under **Peers**, click **Add Peer**, enter `10.6.0.2`, check **HSS** and **PCRF**, and click **Add**.
- Under **HSS**, click the edit icon next to **HSS Realm**, enter `epc.mnc001.mcc001.3gppnetwork.org`, and click **Update**.
- Under **Voice QoS**, click the edit icon next to **Policy Function**, select **PCRF**, and click **Update**.
- Click the edit icon next to **PCRF Realm**, enter `epc.mnc001.mcc001.3gppnetwork.org`, and click **Update**.

Under **Peers**, Ella Core should show as **open**.

## 4. Route the phones' traffic

On the Ella IMS machine, route the phones' voice addresses, which we create in the next step, through Ella Core:

```shell
sudo ip route add 10.46.0.0/16 via 10.6.0.2 mtu lock 1400
```

## 5. Prepare Ella Core for voice

In the Ella Core UI, go to the **Operator** page and scroll to the **IMS** section. Click the edit icon next to **P-CSCF Addresses**, enter `10.6.0.5`, and click **Update**.

Go to the **Networking** page and open the **Data Networks** tab. Click **Create**, enter `ims` as the **Name**, `10.46.0.0/16` as the **IPv4 Pool**, turn on **IMS (voice and mission critical services)**, and click **Create**.

Go to the **Profiles** page and click on the `default` profile. Click **Add Policy**, enter `voice` as the **Name**, set the **Data Network** to `ims`, and click **Create**.

Go to the **Subscribers** page. For each of the two subscribers, click on it, click the edit icon next to **MSISDN**, enter a number, and click **Update**:

- First subscriber: `+15551230001`
- Second subscriber: `+15551230002`

## 6. Register the phones

On each phone, turn on VoNR, then turn airplane mode on and off.

In Ella IMS, go to the **Registrations** page. You should see both phones.

## 7. Make a call

From the first phone, call `+15551230002`. Answer on the second phone, talk, and hang up.

In Ella IMS, go to the **Calls** page. You should see the call, with the outcome **answered**.

!!! success

    Congratulations! You have made a voice call over your private 5G network, through Ella Core and Ella IMS.
