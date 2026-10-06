UPDATE sip:a87a2e9a-4c32-439f-afc0-825ea2f8b236@10.46.0.9:43885 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKPQ44FYUSF7AXN5HJUXY3WHFKCF
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKIK45LIBR67JUYECPELGGFANVLJ
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK2LBMED4VHO4Y2GON5G5HLL2UUC
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bK7VEOJPC43DMM6MPNSCWYRW65CS
From: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=111227338
To: <tel:15550000008;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=111227617
CSeq: 111227333 UPDATE
Call-ID: 111227331_1761235264@10.46.0.8
Content-Length: 471
Via: SIP/2.0/TCP 10.46.0.8:42225;branch=z9hG4bK2328402568
Record-Route: <sip:nsbgwhufa64gwwi7ngbk7jry6d@10.80.0.5:5063;transport=tcp;lr;r2=on;did=MWLT43YCLCK6R2XV;ue>
Record-Route: <sip:nsbgwhufa64gwwi7ngbk7jry6d@10.80.0.5:5060;lr;r2=on;did=MWLT43YCLCK6R2XV>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=3AXXJPBTDDIYAIF7>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=SVRAV3EN7G2CGUU2>
Record-Route: <sip:l2aykhrmqrls3wwcj6g7e6j3yy@10.80.0.5:5060;lr;r2=on;did=ZVKFOBV3FEVOSXZN>
Record-Route: <sip:l2aykhrmqrls3wwcj6g7e6j3yy@10.80.0.5:5063;transport=tcp;lr;r2=on;did=ZVKFOBV3FEVOSXZN;ue>
Max-Forwards: 66
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Type: application/sdp
Contact: <sip:2da86196-c8f7-4939-af8b-fbdac4e983f2@10.46.0.8:42225>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: precondition

v=0
o=- 20 1001 IN IP4 10.46.0.8
s=QC VOIP
c=IN IP4 10.46.0.8
b=AS:37
b=RS:600
b=RR:2000
t=0 0
m=audio 50012 RTP/AVP 104 96
b=AS:37
b=RS:600
b=RR:2000
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-set=3,4,5,6;mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=curr:qos local sendrecv
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
