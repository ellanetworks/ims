# SMS over IP

Phones on IMS can send and receive SMS as SIP MESSAGEs instead of over the 4G or 5G signalling (NAS). The IP Short Message Gateway (IP-SM-GW) is the Application Server that carries them between the phones and the SMSC.

## A server inside the IMS

Ella IMS includes an IP-SM-GW, which runs in the same process as the CSCFs. Its name is `sip:ipsmgw.<home domain>`. The HSS's filter criteria decide which subscribers it serves: without them, it receives nothing, and filter criteria naming another server bring in an external IP-SM-GW instead.

## Registration

When a phone registers, the S-CSCF tells the IP-SM-GW, which follows the phone's registration. While the phone advertises SMS over IP, the IP-SM-GW registers itself at the HSS for the subscriber. The HSS then sends the routing requests of the subscriber's incoming SMS to the IP-SM-GW. When the phone no longer advertises it, or deregisters, the IP-SM-GW deregisters too.

## Sending

The phone's SMS reaches the IP-SM-GW through the S-CSCF, and the IP-SM-GW forwards it to the SMSC of the service centre address the phone uses. The SMSC's answer goes back to the phone as a submit report. Without an SMSC, the IP-SM-GW refuses the SMS, and the phone sends it over NAS.

## Receiving

The IP-SM-GW delivers an incoming SMS over IMS first, if the phone advertises SMS over IP, then through the MME or SMSF the HSS names.
