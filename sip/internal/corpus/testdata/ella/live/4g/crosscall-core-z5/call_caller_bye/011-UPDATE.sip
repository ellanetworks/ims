UPDATE sip:ca20d2b6-2524-424e-ac97-40fff9783fc4@10.46.0.15:43435 SIP/2.0
From: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=194782
To: <tel:15550000004;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=194988
CSeq: 194777 UPDATE
Call-ID: 194775_1760061488@10.46.0.13
Content-Length: 455
Via: SIP/2.0/TCP 10.46.0.13:40279;branch=z9hG4bK2363072888
Max-Forwards: 70
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015EE0000
Security-Verify: ipsec-3gpp;q=0.1;prot=esp;mod=trans;spi-c=51912239;spi-s=169938931;port-c=5064;port-s=5063;alg=hmac-sha-1-96;ealg=null
Route: <sip:mwfvpmdjn5gmmwmnbhkzroke5x@10.80.0.5:5063;transport=tcp;lr;r2=on;did=I3VEOOSXBXWVRD4B;ue>,<sip:mwfvpmdjn5gmmwmnbhkzroke5x@10.80.0.5:5060;lr;r2=on;did=I3VEOOSXBXWVRD4B>,<sip:mo@10.80.0.5:5080;lr;did=YWBH6GTYVJ5VQAZV>,<sip:mt@10.80.0.5:5080;lr;did=WKWWPFMDML32JVYA>,<sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=HJK2HADAP5QEYTHG>,<sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=HJK2HADAP5QEYTHG;ue>
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Type: application/sdp
Require: sec-agree,precondition
Proxy-Require: sec-agree
Contact: <sip:6360b6ba-8a1f-493d-a05a-f4121465d17e@10.46.0.13:40279>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi

v=0
o=- 4 1001 IN IP4 10.46.0.13
s=QC VOIP
c=IN IP4 10.46.0.13
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50014 RTP/AVP 104 96
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
