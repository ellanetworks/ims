# SIP Trunks

A SIP trunk connects Ella IMS to a carrier, for calls to and from numbers outside the HSS.

SIP trunks

## The border

- **BGCF**: picks the trunk for a call out.
- **IBCF**: the border with carriers. It hides the IMS from them.
- **TrGW**: relays the voice.

## Calls out

1. A phone calls a number that the HSS doesn't know.
1. Ella IMS takes the route with the longest matching prefix, else the default route.
1. It tries the route's trunks in order, skipping those that are down or disabled.

Short codes never go to a trunk.

## Calls in

Ella IMS finds the trunk by the call's source address, and routes the call to the subscriber with the called number.

## Trust

Ella IMS accepts the caller's number only from a trusted trunk. Mark a trunk trusted only if you have an interconnect agreement with the carrier.

## Voice

Ella IMS relays the voice without transcoding, so phones and the carrier must share a codec, such as AMR-WB. A carrier that accepts only G.711 needs a transcoding SBC.

## Keepalives

Ella IMS sends OPTIONS requests to each trunk. A trunk that stops answering is down until it answers again.

## Not supported

- Emergency calls
- Video, fax and messages
- Registration with the carrier
- SRTP
