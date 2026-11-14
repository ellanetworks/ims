# Integrate with Open5GS

This guide connects Ella IMS to an Open5GS 5G core, with Open5GS as the HSS and the PCF.

## Prerequisites

- **Ella IMS**: installed and started, as in [Install](https://docs.ellanetworks.com/ims/how_to/install/index.md).
- **Open5GS**: a 5G core set up as in its [Quickstart](https://open5gs.org/open5gs/docs/guide/01-quickstart/), built from `main` or a release after v2.8.0.

The examples use these addresses:

| Address        | Role                           |
| -------------- | ------------------------------ |
| `10.0.0.10`    | Open5GS's address              |
| `10.0.0.20`    | Ella IMS's address             |
| `10.45.0.0/16` | IP pool of Open5GS's `ims` DNN |

## 1. Set the Ella IMS addresses

In `ims.yaml`, set the address phones and Open5GS reach Ella IMS on, its Diameter host label, and the address it receives the PCF's notifications on, and restart Ella IMS:

```
sip:
  addresses: [10.0.0.20]
diameter:
  address: 10.0.0.20
  host_label: ims
n5:
  notify:
    address: 10.0.0.20
```

## 2. Add Open5GS as the HSS and PCF

In the Ella IMS UI, go to the **Cores** page:

- Under **Peers**, click **Create Peer**, enter `10.0.0.10`, check **HSS**, and click **Create**.
- Under **HSS**, click the edit icon next to **HSS Realm**, enter `localdomain`, and click **Update**.
- Under **Voice QoS**, click the edit icon next to **Policy Function**, select **PCF**, enter `http://10.0.0.10:7777` as the **PCF URI**, and click **Update**.

## 3. Route the phones' traffic

On the Ella IMS host, route the `ims` DNN through Open5GS's address:

```
sudo ip route add 10.45.0.0/16 via 10.0.0.10 mtu lock 1400
```

## 4. Configure Open5GS

At the end of `/etc/freeDiameter/hss.conf`, add Ella IMS as the peer `ims.ims.mnc<MNC>.mcc<MCC>.3gppnetwork.org`, with the MNC padded to 3 digits, e.g. for `001` / `01`:

```
ConnectPeer = "ims.ims.mnc001.mcc001.3gppnetwork.org" { ConnectTo = "10.0.0.20"; No_TLS; No_SCTP; };
```

In `/etc/open5gs/pcf.yaml`, change the address of `pcf.sbi.server` from `127.0.0.13` to `10.0.0.10`.

In `/etc/open5gs/smf.yaml`, under `smf`, add:

```
  p-cscf:
    - 10.0.0.20
```

Restart Open5GS:

```
sudo systemctl restart open5gs-hssd open5gs-pcfd open5gs-smfd
```

Exempt Ella IMS from the Quickstart's NAT rule:

```
sudo iptables -t nat -I POSTROUTING -s 10.45.0.0/16 -d 10.0.0.20 -j ACCEPT
```

## 5. Provision the subscribers

In the Open5GS WebUI, for each subscriber:

- Set the **MSISDN**.
- Add an `ims` DNN with 5QI `5` and ARP `1`.
- Under the `ims` DNN, add a PCC rule with 5QI `1`, ARP `2`, and MBR and GBR of `128` Kbps.
