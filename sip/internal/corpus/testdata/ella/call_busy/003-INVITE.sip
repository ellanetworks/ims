INVITE sip:45c5aa86-bf70-11f1-81ff-2bcf73babbc9@10.0.0.3:33445 SIP/2.0
Via: SIP/2.0/TCP 10.0.0.1:5063;branch=z9hG4bKCFQK346LIBUQ2ZCKWEOFPO2ZOB
Via: SIP/2.0/UDP 10.0.0.1:5080;branch=z9hG4bKEX2NOAS3FPDM4MR7LD6KGN7SV5
Via: SIP/2.0/UDP 10.0.0.1:5070;branch=z9hG4bKO2DLODNYYYDQWIIY5MPETVMOMK
Via: SIP/2.0/UDP 10.0.0.1:5080;branch=z9hG4bK72HVB67VBHIBV7KGYTWFOEPHFV
Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bK7QOZ5JAMSZANFAT4FB7RT5PNLZ
Via: SIP/2.0/TCP 10.0.0.2:34253;branch=z9hG4bKO46ANPQZZKQ2AKCVUHJHWPBHOC;rport=38225;received=10.0.0.2
Record-Route: <sip:vnr7bmqfxnmbaexcy6fhcmncpf@10.0.0.1:5063;transport=tcp;lr;r2=on;did=YAYSJNZSK2PBFAYD;ue>
Record-Route: <sip:vnr7bmqfxnmbaexcy6fhcmncpf@10.0.0.1:5060;lr;r2=on;did=YAYSJNZSK2PBFAYD>
Record-Route: <sip:mt@10.0.0.1:5080;lr;did=AQVLX5OMUJRSQJNC>
Record-Route: <sip:mo@10.0.0.1:5080;lr;did=3RTA6P4SFCC6YD3B>
Record-Route: <sip:lcvkhad6e5wvnlost4ata7aziz@10.0.0.1:5060;lr;r2=on;did=OB2A47JR3MF2LN4J>
Record-Route: <sip:lcvkhad6e5wvnlost4ata7aziz@10.0.0.1:5063;transport=tcp;lr;r2=on;did=OB2A47JR3MF2LN4J;ue>
Content-Length: 441
Max-Forwards: 65
From: <sip:+15550001@ims.mnc001.mcc001.3gppnetwork.org>;tag=I4L2GFICJSGDOIL7QP3UOQFRPQ
To: <sip:+15550002@ims.mnc001.mcc001.3gppnetwork.org;user=phone>
Call-ID: 45d57090-bf70-11f1-81ff-2bcf73babbc9@10.0.0.2
CSeq: 1 INVITE
Contact: <sip:45c429ec-bf70-11f1-81ff-2bcf73babbc9@10.0.0.2:34253>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
Supported: 100rel, timer
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, PRACK, NOTIFY, MESSAGE
Accept: application/sdp, application/3gpp-ims+xml
Session-Expires: 1800
Content-Type: application/sdp
P-Called-Party-ID: <tel:+15550002>
P-Early-Media: supported
P-Asserted-Identity: <sip:+15550001@ims.mnc001.mcc001.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550001>

v=0
o=- 117379067674631 0 IN IP4 10.0.0.2
s=-
c=IN IP4 10.0.0.2
t=0 0
m=audio 40000 RTP/AVP 116 118 111 110
b=AS:41
b=RS:512
b=RR:1537
a=rtpmap:116 AMR-WB/16000/1
a=fmtp:116 mode-change-capability=2;max-red=220
a=rtpmap:118 AMR/8000/1
a=fmtp:118 mode-change-capability=2;max-red=220
a=rtpmap:111 telephone-event/16000
a=fmtp:111 0-15
a=rtpmap:110 telephone-event/8000
a=fmtp:110 0-15
a=sendrecv
a=ptime:20
a=maxptime:240
