# Enable Push-to-Talk

This guide enables group calls with floor control with Ella IMS's built-in Mission Critical Push To Talk (MCPTT) server. The examples use `10.6.0.5` as Ella IMS's SIP address.

## Prerequisites

- **Ella Core**: integrated with Ella IMS, as in [Integrate with Ella Core](https://docs.ellanetworks.com/ims/how_to/integrate_with_ella_core/index.md), and serving as the HSS.
- **Phones**: the Ella MCPTT app, installed as in [Install the Android MCPTT App](https://docs.ellanetworks.com/ims/how_to/install_android_app/index.md), and a native IMS client that registers with a `reg-id`.
- **SIMs**: with an ISIM you can program.

## 1. Enable the MCPTT server

In `ims.yaml`, set the address phones reach the MCPTT media on, and restart Ella IMS:

```
mcptt:
  media:
    address: 10.6.0.5
```

## 2. Configure Ella Core

In the Ella Core UI:

- **Networking → Data Networks → Create**: `mcx`, with an IP pool and **IMS** on.
- **Profiles → Create**: an MC profile, e.g. `mcptt`. On it, **Add Policy**: a name, data network `mcx`, 5QI/QCI `69`, ARP `3`, **May pre-empt**, **Not pre-emptable**.
- On the MC profile, also add the policies the users had on their previous profile, e.g. `internet` and `ims`.
- **Operator → IMS → Add Application Server**: click **Fill for Ella IMS MCPTT**, then **Add**.
- **Operator → IMS → Add Service Profile**: name `mcptt`; under **Add from preset**, select the **MCPTT** preset and click **Add Criteria**, then click **Add**.
- **Profiles →** the MC profile **→** edit **IMS Service Profiles**: set **MCPTT** to `mcptt`, and **MMTel (voice, SMS)** to the service profile of the users' previous profile, if any, then **Save**.
- **Subscribers**: put each user on the MC profile, with an MSISDN.

## 3. Route the MC traffic

On the Ella IMS host, route the `mcx` IP pool through Ella Core's N6 address, e.g.:

```
sudo ip route add 10.47.0.0/16 via 10.6.0.2 mtu lock 1400
```

## 4. Add the users

In the Ella IMS UI, go to **Push-to-Talk → Users**. For each person, click **Add User** and enter:

- **MCPTT ID**: e.g. `alice`.
- **IMS Identities**: the subscriber's MSISDN, e.g. `15550000001`.

## 5. Add a group

Go to **Push-to-Talk → Groups**, click **Add Group**, and enter a group ID, e.g. `fire`, and its members.

## 6. Set up the phones

On each phone:

- Program the SIM's ISIM with `sip:mcptt-<MSISDN>@<home domain>` in a record of EF.IMPU after the first.
- Set the APN to `mcx`.
- In the app's settings, set the user's MCPTT ID as Ella IMS shows it, e.g. `alice@ims.mnc001.mcc001.3gppnetwork.org`, and groups.

## 7. Make a group call

Start a group call from one phone. The other members' phones should ring, and **Push-to-Talk → Calls** should show the call.
