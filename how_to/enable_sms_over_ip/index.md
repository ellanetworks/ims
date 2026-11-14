# Enable SMS over IP

This guide lets phones send and receive SMS over IMS with Ella IMS's built-in IP-SM-GW. The examples use `10.3.0.7` as the SMSC's Diameter address and `15550000000` as its service centre address.

## Prerequisites

- **HSS**: an HSS connected to Ella IMS that supports the Sh-Update of SMSRegistrationInfo and forwards routing requests to the IP-SM-GW over S6c.
- **SMSC**: an SMSC with an SGd interface, such as Ella SMSC.
- **MME or SMSF**, for delivery over NAS when IMS fails: reachable over SGd.

## 1. Set the IP-SM-GW number

In the Ella IMS UI, go to the **SMS over IP** page. Under **IP-SM-GW**, click the edit icon next to **Number**, enter an E.164 number without `+`, e.g. `15550000100`, and click **Update**.

## 2. Add the SMSC peer

Go to the **Cores** page. Under **Peers**, click **Create Peer**, and enter:

- **Address**: `10.3.0.7`, or the SMSC's host name, e.g. `smsc-0.smsc.ella.svc.cluster.local`.
- **Host**: the SMSC's Diameter identity, e.g. `smsc.node.epc.mnc001.mcc001.3gppnetwork.org` for Ella SMSC.
- **Roles**: **SGd** only.
- **Transport**: **SCTP**, for Ella SMSC.

Click **Create**. For an Ella SMSC cluster, create a peer for each node, with the node's address and host.

## 3. Let the HSS reach the IP-SM-GW

On the **Cores** page, edit the HSS peer, check **Sh** and **S6c**, and click **Update**. If the HSS's core also serves the MME or SMSF, check **SGd** too, for fallback to SMS over NAS.

## 4. Route the service centre to the SMSC

Go to the **SMS over IP** page. Under **Routes**, click **Create Route**, and enter:

- **Service Centre**: `15550000000`, or leave empty for the default route.
- **SMSC Peers**: check the SMSC peers.

Click **Create**.

## 5. Add iFCs in the HSS

With Ella Core:

- **Operator → IMS → Add Application Server**: name `ip-sm-gw`, the **Name** of the **IP-SM-GW** on the **SMS over IP** page as the **Server Name**, then **Add**.
- **Operator → IMS → Add Service Profile**: name `sms-over-ip`; under **Add from preset**, select the **SMS over IP** preset and the `ip-sm-gw` **Application Server**, click **Add Criteria**, then click **Add**.
- **Profiles →** the subscribers' profile **→** edit **IMS Service Profiles**: set **MMTel (voice, SMS)** to `sms-over-ip`, then **Save**.

With another HSS, add two iFCs to the subscribers' service profiles, with the **Name** of the **IP-SM-GW** on the **SMS over IP** page as the server name, e.g. `sip:ipsmgw.ims.mnc001.mcc001.3gppnetwork.org`, and default handling `SESSION_CONTINUED`:

- A trigger point on method `REGISTER`, with the subscriber's MSISDN as the service information, and **Include Register Request** and **Include Register Response** set.
- A trigger point on method `MESSAGE`, session case `ORIGINATING_REGISTERED`, and header `Content-Type` matching `application/vnd\.3gpp\.sms`.

## 6. Verify

Register a phone with SMS over IP enabled, and send an SMS. `ellaims_ipsmgw_short_messages_total` should count it, with direction `mo` and result `success`.
