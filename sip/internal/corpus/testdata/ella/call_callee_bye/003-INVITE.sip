INVITE sip:4542e7f2-bf70-11f1-81ff-2bcf73babbc9@10.0.0.3:35047 SIP/2.0
Via: SIP/2.0/TCP 10.0.0.1:5063;branch=z9hG4bKWBIMPSBLNEUCLS2PEXWAQTRZHA
Via: SIP/2.0/UDP 10.0.0.1:5080;branch=z9hG4bKFBWQLIQGD2N4NX3ZT56LTXFZDL
Via: SIP/2.0/UDP 10.0.0.1:5070;branch=z9hG4bKBV5Q3IHGATOUEBPDAO3GUCAPX6
Via: SIP/2.0/UDP 10.0.0.1:5080;branch=z9hG4bKFW7XECO3D45ZPRJPPQLYPEPPAC
Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bKCSGZKNAGSTRSX4HUM7V74DZXUS
Via: SIP/2.0/TCP 10.0.0.2:38873;branch=z9hG4bKLRWYEUC4NQH6PVNXVBM3VB3QBM;rport=45067;received=10.0.0.2
Record-Route: <sip:dmigbowgjhlyfhiyff53dy5zs3@10.0.0.1:5063;transport=tcp;lr;r2=on;did=FEYKJQXXSNVWCP2S;ue>
Record-Route: <sip:dmigbowgjhlyfhiyff53dy5zs3@10.0.0.1:5060;lr;r2=on;did=FEYKJQXXSNVWCP2S>
Record-Route: <sip:mt@10.0.0.1:5080;lr;did=M2AXKKAWOOTF57UT>
Record-Route: <sip:mo@10.0.0.1:5080;lr;did=LT3U4N66E27HNAHE>
Record-Route: <sip:7qi7n3eiqts4aak3xd3aop3cjh@10.0.0.1:5060;lr;r2=on;did=5VQT5XXRKLQRRCPG>
Record-Route: <sip:7qi7n3eiqts4aak3xd3aop3cjh@10.0.0.1:5063;transport=tcp;lr;r2=on;did=5VQT5XXRKLQRRCPG;ue>
Content-Length: 441
Max-Forwards: 65
From: <sip:+15550001@ims.mnc001.mcc001.3gppnetwork.org>;tag=HSE7SBOTEW6WSVPZ2JIJ4LBU6T
To: <sip:+15550002@ims.mnc001.mcc001.3gppnetwork.org;user=phone>
Call-ID: 45731f74-bf70-11f1-81ff-2bcf73babbc9@10.0.0.2
CSeq: 1 INVITE
Contact: <sip:4540d1d8-bf70-11f1-81ff-2bcf73babbc9@10.0.0.2:38873>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
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
o=- 117379067674627 0 IN IP4 10.0.0.2
s=-
c=IN IP4 10.0.0.2
t=0 0
m=audio 40002 RTP/AVP 116 118 111 110
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
