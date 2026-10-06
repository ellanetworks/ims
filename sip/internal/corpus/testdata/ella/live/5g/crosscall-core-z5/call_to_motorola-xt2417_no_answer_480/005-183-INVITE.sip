SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKQSPS7PAX3TZJAIWZDRGFEHMKGP,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKNKBYB2UQGFZ5TSAA7HZ627CXMJ,SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bK3C2Y35OZXJ26RTHXSYQSHS4WHD,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKSDXCNOUZTW3SZ6SSOKQ3X5QPHL,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKBXUP2B2ZZ4MBKM6KSUQ2KMHXKG,SIP/2.0/TCP 10.46.0.8:42225;branch=z9hG4bK735318805
From: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=111319409
To: <tel:15550000008;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=111319698
CSeq: 111319402 INVITE
Call-ID: 111319402_1761473040@10.46.0.8
Record-Route: <sip:nsbgwhufa64gwwi7ngbk7jry6d@10.80.0.5:5063;transport=tcp;lr;r2=on;did=KEIQ7AUMVIAEI7KE;ue>,<sip:nsbgwhufa64gwwi7ngbk7jry6d@10.80.0.5:5060;lr;r2=on;did=KEIQ7AUMVIAEI7KE>,<sip:mt@10.80.0.5:5080;lr;did=XFM6GZDIASPZJWPX>,<sip:mo@10.80.0.5:5080;lr;did=4BHLROODLRCP3SS5>,<sip:l2aykhrmqrls3wwcj6g7e6j3yy@10.80.0.5:5060;lr;r2=on;did=6ELVBEC7G6227QEB>,<sip:l2aykhrmqrls3wwcj6g7e6j3yy@10.80.0.5:5063;transport=tcp;lr;r2=on;did=6ELVBEC7G6227QEB;ue>
Content-Type: application/sdp
Content-Length: 494
Server: PRD-IR92/12 term-Motorola/XT2417-1-V1UFN35H.193-20 device-type/smart-phone
RSeq: 1
P-Access-Network-Info: 3GPP-NR-TDD;utran-cell-id-3gpp=0010100000100066C000
Contact: <sip:a87a2e9a-4c32-439f-afc0-825ea2f8b236@10.46.0.9:43885>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: 100rel,precondition
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS

v=0
o=- 7 1000 IN IP4 10.46.0.9
s=QC VOIP
c=IN IP4 10.46.0.9
b=AS:37
b=RS:600
b=RR:2000
t=0 0
m=audio 50028 RTP/AVP 104 96
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
