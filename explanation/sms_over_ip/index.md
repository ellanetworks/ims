# SMS over IP

Ella IMS includes an IP Short Message Gateway (IP-SM-GW), which lets phones send and receive SMS over IMS.

SMS over IP

## Sending

1. The phone sends the SMS as a SIP MESSAGE, which reaches the IP-SM-GW through the P-CSCF and the S-CSCF.
1. The IP-SM-GW forwards it to the SMSC of the SMS route of the phone's service centre address, or of the default route if no route has that address.

## Receiving

1. While the phone registers with SMS over IP, the IP-SM-GW registers at the HSS for the subscriber.
1. The HSS then sends the SMSC's routing requests for the subscriber to the IP-SM-GW.
1. The IP-SM-GW delivers over IMS first, then over NAS through the MME or SMSF.

## SMS routes

An SMS route maps a service centre address to the SMS-IWMSC that serves it, usually part of the SMSC. TS 29.338 leaves this mapping to local configuration. A route names the SMS-IWMSC by its Diameter host, so its peers need one.

The default route works the same way, for every service centre address no other route has. It cannot hand short messages to a Diameter routing agent to route by realm, another fallback TS 29.338 allows: an SMS-IWMSC behind such an agent needs its own peer.

Routes are separate from peers. A peer is a Diameter connection, shared by every application it serves. A route is a choice of the IP-SM-GW alone. An SMSC's connection appears with the other peers on the **Cores** page; its routes appear on the **SMS over IP** page.
