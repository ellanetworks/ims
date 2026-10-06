UPDATE sip:2da86196-c8f7-4939-af8b-fbdac4e983f2@10.46.0.8:42225 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bK4QMLZBOGROZ3C7WSOSUZFAQUD7
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK6ULXDAQPV3NZPXZTYQSULS7UCD
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKABSXKU4F6PZWACBEUBLL3SGXXB
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKM66O2NJ5LCPGNEYDQZNXPTDHOP
From: <sip:15550000008@ims.mnc001.mcc001.3gppnetwork.org>;tag=110381187
To: <sip:15550000005;phone-context=ims.mnc001.mcc001.3gppnetwork.org@ims.mnc001.mcc001.3gppnetwork.org;user=phone>;tag=110381076
CSeq: 110381184 UPDATE
Call-ID: 110381182_4109868208@10.46.0.9
Content-Length: 450
Via: SIP/2.0/TCP 10.46.0.9:43885;branch=z9hG4bK4179410206
Record-Route: <sip:l2aykhrmqrls3wwcj6g7e6j3yy@10.80.0.5:5063;transport=tcp;lr;r2=on;did=5AX5VLJIYKXJI5TD;ue>
Record-Route: <sip:l2aykhrmqrls3wwcj6g7e6j3yy@10.80.0.5:5060;lr;r2=on;did=5AX5VLJIYKXJI5TD>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=UBVOTASGC3S2Q5JR>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=S3IWEBVY7OHMTTVY>
Record-Route: <sip:nsbgwhufa64gwwi7ngbk7jry6d@10.80.0.5:5060;lr;r2=on;did=7D5MQKY37NSIRTAU>
Record-Route: <sip:nsbgwhufa64gwwi7ngbk7jry6d@10.80.0.5:5063;transport=tcp;lr;r2=on;did=7D5MQKY37NSIRTAU;ue>
Max-Forwards: 66
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
User-Agent: PRD-IR92/12 term-Motorola/XT2417-1-V1UFN35H.193-20 device-type/smart-phone
Content-Type: application/sdp
Contact: <sip:a87a2e9a-4c32-439f-afc0-825ea2f8b236@10.46.0.9:43885>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: precondition

v=0
o=- 1 1001 IN IP4 10.46.0.9
s=QC VOIP
c=IN IP4 10.46.0.9
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50032 RTP/AVP 99 96
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:99 AMR-WB/16000/1
a=fmtp:99 mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=curr:qos local sendrecv
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
