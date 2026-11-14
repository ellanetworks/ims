# Connect to a SIP Trunk

This guide connects Ella IMS to a carrier's SIP trunk, for calls to and from numbers outside the HSS. The example carrier is [Telnyx](https://telnyx.com), any other carrier should work similarly.

The examples use these addresses:

| Address                                          | Role                                   |
| ------------------------------------------------ | -------------------------------------- |
| `203.0.113.5`                                    | Ella IMS's address towards the carrier |
| `sip.telnyx.com`                                 | Telnyx's SIP domain                    |
| `192.76.120.10`, `64.16.250.10`, `192.76.120.14` | Addresses Telnyx sends calls from      |

## Prerequisites

- **Ella IMS**: connected to the core network, as in [Integrate with Ella Core](https://docs.ellanetworks.com/ims/how_to/integrate_with_ella_core/index.md) or [Integrate with Open5GS](https://docs.ellanetworks.com/ims/how_to/integrate_with_open5gs/index.md), with the **Numbering Plan** set on the **Operator** page.
- **SIP trunk**: an IP-authenticated trunk.
- **Numbers**: the carrier's numbers, set as the subscribers' MSISDNs in the HSS.

## 1. Enable the IBCF

In `ims.yaml`, set the address carriers reach Ella IMS on, and restart Ella IMS:

```
ibcf:
  external:
    addresses:
      - address: 203.0.113.5
```

In a cluster, set each node's own address on that node.

## 2. Open the firewall

Allow SIP (TCP `5060`) from Telnyx's addresses, and voice (UDP `10000`–`19999`) from [Telnyx's media networks](https://sip.telnyx.com), to `203.0.113.5`. In a cluster, do the same for each node's address.

## 3. Configure the carrier

In the carrier portal, create an IP connection:

- Send calls to `203.0.113.5`, port `5060`, over TCP. In a cluster, add each node's address, or a host name that resolves to all of them.
- Enable AMR-WB.
- Send called numbers in E.164 format, with `+`.
- Attach an outbound voice profile.
- Assign your numbers to the connection.

## 4. Add the trunk

In the Ella IMS UI, go to the **SIP Trunks** page and click **Create Trunk**:

- **Name**: `Telnyx`.
- **Host**: `sip.telnyx.com`.
- **Inbound CIDRs**: `192.76.120.10/32`, `64.16.250.10/32` and `192.76.120.14/32`, one per line.
- Check **Trusted**.

Click **Create**.

## 5. Add a route

On the **SIP Trunks** page, click **Create Route**. Leave **Prefix** empty, select `Telnyx` under **Add Trunk**, and click **Create**.

## 6. Check the trunk is up

On the **SIP Trunks** page, the trunk's **State** should be `up` within a minute.

## 7. Place a test call

From a registered phone, call an outside number in international format, e.g. `+15555550100`. Then call one of your numbers from an outside phone.

Both calls should have audio both ways.
