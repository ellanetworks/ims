---
description: Step-by-step instructions to connect an Application Server to Ella IMS.
---

# Integrate with an Application Server

This guide connects an Application Server (AS), such as voicemail or conferencing, to Ella IMS. The examples use `10.80.0.30` as the AS's address and `10.6.0.5` as Ella IMS's SIP address.

## Prerequisites

- **HSS**: an HSS that provisions initial Filter Criteria (iFCs).

## 1. Trust the AS

In `ims.yaml`, add the AS's address to the trusted networks, optionally set how long the AS has to answer, and restart Ella IMS:

```yaml
sip:
  trusted_networks: [10.80.0.30/32]
scscf:
  as_timeout: 4s
```

## 2. Add iFCs in the HSS

In the HSS, add iFCs to the subscribers' service profiles, with the AS's SIP URI as the server name, e.g. `sip:10.80.0.30:5060`:

- For third-party registration, add a trigger point on method `REGISTER`. Set **Include Register Request** and **Include Register Response** if the AS needs them.
- For originating or terminating services, add a trigger point on the session case, plus the method, header or Request-URI to match.
- Set the default handling to `SESSION_CONTINUED` to go on without the AS, or to `SESSION_TERMINATED` to fail the call, or deregister the user on `REGISTER`.

## 3. Add the AS's PSIs

If the AS serves its own URIs, such as a conference factory:

- Add its PSIs to the HSS, distinct or wildcarded, with their iFCs or the AS as their server name.
- If the server name is a host name, make it resolve to the AS, with a DNS A or SRV record.

## 4. Configure the AS

On the AS:

- Trust Ella IMS's SIP address, `10.6.0.5`.
- Send requests to a user to the I-CSCF, port `5070`.
- Send requests on behalf of a user or a PSI to the S-CSCF, port `5080`, with `orig` in the Route header, `Route: <sip:10.6.0.5:5080;lr;orig>`, and the user in P-Served-User or P-Asserted-Identity.

## 5. Verify

Register a phone. The AS should receive a third-party REGISTER, or the first request that matches its iFCs.
