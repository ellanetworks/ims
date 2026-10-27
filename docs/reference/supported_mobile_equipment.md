---
description: Reference for the core networks, phones and clients validated with Ella IMS.
---

# Supported Mobile Equipment

Ella IMS follows 3GPP standards. Any compliant core network, Application Server and phone should work with it. This page lists those validated against Ella IMS.

## Core Networks

- **[Ella Core](https://docs.ellanetworks.com/core/)**
- **[Open5GS](https://open5gs.org/)**

## Phones

- **Apple iPhone 11**
- **Apple iPhone 16**
- **Crosscall Core-Z5**
- **Google Pixel 10a**
- **Motorola Moto G 5G**
- **Samsung Galaxy A56**

## MCPTT Clients

- **Ella MCPTT for Android**, in the [`android`](https://github.com/ellanetworks/ims/tree/main/android) directory.

## Phone Quirks

| Phone              | RAT | Observed |
|--------------------|-----|----------|
| Apple iPhone 11    | 4G  | Live Voicemail answers declined and unanswered calls (`200 OK`). |
| Apple iPhone 16    | 4G  | Live Voicemail answers declined and unanswered calls (`200 OK`). |
|                    | 4G  | 3 IMS addresses in 6 minutes. |
|                    | 4G  | No video calls: registers with `audio` only, no `video` feature tag. |
|                    | 5G  | 5G SA unavailable without SUCI on the SIM (USIM service 124, `EF.SUCI_Calc_Info`). |
|                    | 5G  | Rejects NEA0 (Security Mode Reject, cause 24). |
|                    | 5G  | SIM PLMN 001/01: never attempts the 5G SA cell. |
|                    | 5G  | SIM PLMN 999/01: data only, no voice settings, no IMS PDU session. |
| Google Pixel 10a   | 4G  | Video call to an iPhone: the dialer offers Google Meet instead. |
| Motorola Moto G 5G | 5G  | SIM PLMN 999/01: 5G SA only with network type "NR only". |
|                    | 5G  | Video call answered without a camera, then switched to `recvonly` (once in 5). |
|                    | 4G  | SIM PLMN 999/01: IMS doesn't start. |
| Samsung Galaxy A56 | 4G  | SIM PLMN 001/01: VoLTE off (`LABSIM` profile, TS.43 entitlement 403). |
