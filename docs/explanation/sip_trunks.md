---
description: How Ella IMS connects calls to other networks over SIP trunks.
---

# SIP Trunks

A SIP trunk connects Ella IMS to a carrier, such as a PSTN provider or another operator's IMS. It carries calls to and from numbers outside the HSS.

<figure markdown="span">
  ![SIP trunks](../images/sip_trunk.svg){ width="800" }
  <figcaption>SIP trunks</figcaption>
</figure>

## The border

3GPP puts three functions at the border of an IMS:

- **BGCF**: picks the trunk a call leaves through.
- **IBCF**: the border itself. It hides the IMS from carriers, and checks what crosses.
- **TrGW**: relays the voice of the calls that cross.

Ella IMS runs all three in its one process.

## Calls out

1. A phone calls a number that the HSS doesn't know.
2. The BGCF takes the route with the longest prefix of the number, else the default route.
3. The IBCF tries the route's trunks in order. It skips trunks that are down or disabled.
4. If a trunk fails before any provisional response, the IBCF tries the next server, then the next trunk.

If every trunk of the route is down or disabled, the call fails. It doesn't fall back to a shorter prefix.

Only numbers dialled in full go to a trunk: in international form, in national form, or long enough. Short codes stay inside the IMS, and so do numbers of fewer than 7 or more than 15 digits, country code included.

## Calls in

1. A carrier sends a call to the IBCF.
2. The IBCF finds the trunk by the call's source address.
3. The I-CSCF routes the call to the subscriber with that number, as any other call.

A disabled trunk refuses calls in, as well as calls out.

## Trust

A carrier with an interconnect agreement can be trusted. Ella IMS then accepts the caller's number it asserts. From an untrusted carrier, the caller's number is only a claim. See [Security](security.md#sip-trunks).

Ella IMS asserts the caller's number to every carrier, trusted or not. A caller who hides their number hides it from an untrusted carrier. A trusted carrier gets the number, with the request to keep it private.

On a forwarded call, the number asserted is that of the subscriber who forwarded the call, and the carrier learns that this subscriber forwarded it. A forwarding subscriber who keeps their history private stays hidden from an untrusted carrier.

A trunk's inbound CIDRs never overlap the IMS's own networks. A trunk that overlaps them after a change to the configuration file carries no calls, and its state is `unusable`. So does a trunk that requires TLS when the configuration file has no certificate for the IBCF.

## Voice

Ella IMS relays the voice of every call over a trunk. Phones and carriers only see Ella IMS's addresses, never each other's.

The voice is relayed as it is, without transcoding. The phone and the carrier must share a codec, such as AMR-WB, which Telnyx accepts. Phones don't offer G.711, so a carrier that accepts only G.711 needs an SBC that transcodes.

## Keepalives

Ella IMS sends OPTIONS requests to each trunk with a keepalive interval. A trunk that stops answering is down, and routes skip it until it answers again.

Over TLS, Ella IMS keeps a connection open to each of the carrier's servers, with or without OPTIONS requests.

## What SIP trunks don't carry

- **Emergency calls.** Ella IMS doesn't handle emergency calls, and doesn't send them to a trunk.
- Video, fax and messages.
- Registration with the carrier. The carrier knows Ella IMS by its IP address.
- SRTP. Voice crosses unencrypted, even when signalling uses TLS.
