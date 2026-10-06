SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKWXJQVKFWSHE2V3FRO6JG6UPNCE,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKDAXHYBAVBXPMQP4SUJDOEWCM24,SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bK6B6AVX2YQK3SDIR6Q73IRYM5HI,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKEJMLZSXXDDSOJ3TKOLMUY6UNXM,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKACUYJNQ6CYWTTIHHC5HCO7NSOU,SIP/2.0/TCP 10.46.0.8:42225;branch=z9hG4bK449680059
From: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=111227338
To: <tel:15550000008;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=111227617
CSeq: 111227331 INVITE
Call-ID: 111227331_1761235264@10.46.0.8
Record-Route: <sip:nsbgwhufa64gwwi7ngbk7jry6d@10.80.0.5:5063;transport=tcp;lr;r2=on;did=MWLT43YCLCK6R2XV;ue>,<sip:nsbgwhufa64gwwi7ngbk7jry6d@10.80.0.5:5060;lr;r2=on;did=MWLT43YCLCK6R2XV>,<sip:mt@10.80.0.5:5080;lr;did=3AXXJPBTDDIYAIF7>,<sip:mo@10.80.0.5:5080;lr;did=SVRAV3EN7G2CGUU2>,<sip:l2aykhrmqrls3wwcj6g7e6j3yy@10.80.0.5:5060;lr;r2=on;did=ZVKFOBV3FEVOSXZN>,<sip:l2aykhrmqrls3wwcj6g7e6j3yy@10.80.0.5:5063;transport=tcp;lr;r2=on;did=ZVKFOBV3FEVOSXZN;ue>
Content-Type: application/sdp
Content-Length: 494
Server: PRD-IR92/12 term-Motorola/XT2417-1-V1UFN35H.193-20 device-type/smart-phone
RSeq: 1
P-Access-Network-Info: 3GPP-NR-TDD;utran-cell-id-3gpp=0010100000100066C000
Contact: <sip:a87a2e9a-4c32-439f-afc0-825ea2f8b236@10.46.0.9:43885>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: 100rel,precondition
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS

v=0
o=- 6 1000 IN IP4 10.46.0.9
s=QC VOIP
c=IN IP4 10.46.0.9
b=AS:37
b=RS:600
b=RR:2000
t=0 0
m=audio 50016 RTP/AVP 104 96
b=AS:37
b=RS:600
b=RR:2000
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-set=3,4,5,6;mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=conf:qos remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
