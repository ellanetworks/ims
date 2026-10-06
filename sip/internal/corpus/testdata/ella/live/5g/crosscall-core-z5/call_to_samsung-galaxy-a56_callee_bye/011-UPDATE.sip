UPDATE sip:15550000007@10.46.0.7:6600 SIP/2.0
Max-Forwards: 70
To: <tel:15550000007;phone-context=ims.mnc001.mcc999.3gppnetwork.org>;tag=c9048402
From: <sip:15550000008@ims.mnc001.mcc999.3gppnetwork.org>;tag=100513383
Call-ID: 100513376_3069421008@10.46.0.8
CSeq: 100513378 UPDATE
Content-Length: 453
Via: SIP/2.0/TCP 10.46.0.8:41989;branch=z9hG4bK1104116071
P-Access-Network-Info: 3GPP-NR-TDD;utran-cell-id-3gpp=9990100000100066C000
Security-Verify: ipsec-3gpp;q=0.1;prot=esp;mod=trans;spi-c=214302590;spi-s=39422167;port-c=5064;port-s=5063;alg=hmac-sha-1-96;ealg=null
Route: <sip:75xi5p5r4lmyujqtijnkhr24d7@10.80.0.5:5063;transport=tcp;lr;r2=on;did=O4LDYHSICKSZYIHD;ue>,<sip:75xi5p5r4lmyujqtijnkhr24d7@10.80.0.5:5060;lr;r2=on;did=O4LDYHSICKSZYIHD>,<sip:mo@10.80.0.5:5080;lr;did=E6U2IYYL6IBKRX6G>,<sip:mt@10.80.0.5:5080;lr;did=OCQD6PWAEM3MXY4W>,<sip:3iqp554vb5dijnjmjo2u3xbt2t@10.80.0.5:5060;lr;r2=on;did=4NKCGDJOCMC2HPOJ>,<sip:3iqp554vb5dijnjmjo2u3xbt2t@10.80.0.5:5063;transport=tcp;lr;r2=on;did=4NKCGDJOCMC2HPOJ;ue>
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Type: application/sdp
Require: sec-agree,precondition
Proxy-Require: sec-agree
Contact: <sip:cbcd92dc-db6f-4aea-a207-e26b95efb9f2@10.46.0.8:41989>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi

v=0
o=- 7 1001 IN IP4 10.46.0.8
s=QC VOIP
c=IN IP4 10.46.0.8
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50048 RTP/AVP 104 96
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=curr:qos local sendrecv
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
