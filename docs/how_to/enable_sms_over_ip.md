---
description: Step-by-step instructions to enable SMS over IP with Ella IMS's built-in IP-SM-GW.
---

# Enable SMS over IP

This guide lets phones send and receive SMS over IMS with Ella IMS's built-in IP-SM-GW. The examples use `10.3.0.7` as the SMSC's Diameter address and `15550000000` as its service centre address.

## Prerequisites

- **HSS**: an HSS connected to Ella IMS that supports the Sh-Update of SMSRegistrationInfo and forwards routing requests to the IP-SM-GW over S6c.
- **SMSC**: an SMSC with an SGd interface, such as Ella SMSC.
- **MME or SMSF**, for delivery over NAS when IMS fails: reachable over SGd.

## 1. Set the IP-SM-GW number

In the Ella IMS UI, go to the **Cores** page. Under **SMS over IP**, click the edit icon next to **IP-SM-GW Number**, enter an E.164 number without `+`, e.g. `15550000100`, and click **Update**.

## 2. Add the SMSC

Under **Peers**, click **Add Peer**, and enter:

- **Address**: `10.3.0.7`.
- **Roles**: **SGd** only.
- **Service Centres**: `15550000000`.
- **Host**: the SMSC's Diameter identity, e.g. `smsc.epc.mnc001.mcc001.3gppnetwork.org`.

Click **Add**.

## 3. Let the HSS reach the IP-SM-GW

Edit the HSS peer, check **Sh** and **S6c**, and click **Update**. If the HSS's core also serves the MME or SMSF, check **SGd** too.

## 4. Add iFCs in the HSS

In the HSS, add two iFCs to the subscribers' service profiles, with the **IP-SM-GW Name** of the **Cores** page as the server name, e.g. `sip:ipsmgw.ims.mnc001.mcc001.3gppnetwork.org`, and default handling `SESSION_CONTINUED`:

- A trigger point on method `REGISTER`, with the subscriber's MSISDN as the service information, and **Include Register Request** and **Include Register Response** set.
- A trigger point on method `MESSAGE`, session case `ORIGINATING_REGISTERED`, and header `Content-Type` matching `application/vnd\.3gpp\.sms`.

## 5. Verify

Register a phone with SMS over IP enabled, and send an SMS. `ellaims_ipsmgw_short_messages_total` should count it, with direction `mo` and result `success`.
