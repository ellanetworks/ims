SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKZ2H35JSYGHJUVN36QNZBVKTEYP,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKOBDAJVZ7S5RCSY53M3ZXAB6ZAL,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKZYD67LXGD3675NAQETMTEAJYR7,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKZHUJKV4SJ6G74L76DEFHPTIX2C,SIP/2.0/TCP 10.46.0.13:40279;branch=z9hG4bK2363072888
From: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=194782
To: <tel:15550000004;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=194988
CSeq: 194777 UPDATE
Call-ID: 194775_1760061488@10.46.0.13
Content-Length: 459
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=HJK2HADAP5QEYTHG;ue>,<sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=HJK2HADAP5QEYTHG>,<sip:mt@10.80.0.5:5080;lr;did=WKWWPFMDML32JVYA>,<sip:mo@10.80.0.5:5080;lr;did=YWBH6GTYVJ5VQAZV>,<sip:mwfvpmdjn5gmmwmnbhkzroke5x@10.80.0.5:5060;lr;r2=on;did=I3VEOOSXBXWVRD4B>,<sip:mwfvpmdjn5gmmwmnbhkzroke5x@10.80.0.5:5063;transport=tcp;lr;r2=on;did=I3VEOOSXBXWVRD4B;ue>
Content-Type: application/sdp
Server: Core-Z5 N1982U.6.02.09.FR00
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015EE0000
Contact: <sip:ca20d2b6-2524-424e-ac97-40fff9783fc4@10.46.0.15:43435>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: precondition
Supported: timer,100rel,from-change,tdialog
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Session-Expires: 1800;refresher=uas

v=0
o=- 7 1001 IN IP4 10.46.0.15
s=QC VOIP
c=IN IP4 10.46.0.15
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50024 RTP/AVP 104 96
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=curr:qos local sendrecv
a=curr:qos remote sendrecv
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
