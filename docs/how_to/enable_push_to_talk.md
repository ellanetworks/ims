---
description: Step-by-step instructions to enable push-to-talk group calls with Ella IMS's built-in MCPTT server.
---

# Enable Push-to-Talk

This guide enables group calls with floor control with Ella IMS's built-in Mission Critical Push To Talk (MCPTT) server. The examples use `10.6.0.5` as Ella IMS's SIP address.

## Prerequisites

- **Ella Core**: integrated with Ella IMS, as in [Integrate with Ella Core](integrate_with_ella_core.md), and serving as the HSS.
- **Phones**: the Ella MCPTT app, installed as in [Install the Android MCPTT App](install_android_app.md).

## 1. Enable the MCPTT server

In `ims.yaml`, set the address phones reach the MCPTT media on, and restart Ella IMS:

```yaml
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
- **Operator → IMS → Add Service Profile**: name `mcptt`, **Add from preset** the **MCPTT** preset, then **Add**.
- **Profiles →** the MC profile **→** edit **IMS Service Profiles**: set **MCPTT** to `mcptt`, then **Save**.
- **Subscribers**: put each user on the MC profile, with an MSISDN.

## 3. Route the MC traffic

On the Ella IMS host, route the `mcx` IP pool through Ella Core's N6 address, e.g.:

```shell
sudo ip route add 10.47.0.0/16 via 10.6.0.2 mtu lock 1400
```

## 4. Add the users

In the Ella IMS UI, go to **Push-to-Talk → Users**. For each person, click **Add User** and enter:

- **MCPTT ID**: e.g. `alice`.
- **IMS Identities**: the subscriber's MSISDN, e.g. `15550000001`, for its MCPTT identity `sip:mcptt-15550000001@<home domain>`.

## 5. Add a group

Go to **Push-to-Talk → Groups**, click **Add Group**, and enter a group ID, e.g. `fire`, and its members.

## 6. Set up the phones

On each phone:

- Program the SIM's ISIM with the MCPTT identity as the second EF.IMPU record, after `sip:+<MSISDN>@<home domain>`. The app registers it, alongside the phone's own IMS registration.
- Set the APN to `mcx`.
- In the app's settings, set the user's MCPTT ID as Ella IMS shows it, e.g. `alice@ims.mnc001.mcc001.3gppnetwork.org`, and groups.

## 7. Make a group call

Start a group call from one phone. The other members' phones should ring, and **Push-to-Talk → Calls** should show the call.
