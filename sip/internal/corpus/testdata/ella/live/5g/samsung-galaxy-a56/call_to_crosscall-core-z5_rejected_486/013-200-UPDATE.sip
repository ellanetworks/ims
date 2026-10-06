SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKQWHAKN5C62E2OAU62WYLQO4QDD,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKZ3EONIXHNSBDTRGGUBRWBNQSEF,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKO7DVAKFBZP34PCUA3P2E3NZR4I,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKLT3AASQBBNI7RDM4VMXCRPJU73,SIP/2.0/TCP 10.46.0.7:6600;branch=z9hG4bK-524287-1---049c32b76d32c497;rport=6601;transport=TCP;received=10.46.0.7
Record-Route: <sip:75xi5p5r4lmyujqtijnkhr24d7@10.80.0.5:5063;transport=tcp;lr;r2=on;did=2AXOPYHCFZJCPDGI;ue>,<sip:75xi5p5r4lmyujqtijnkhr24d7@10.80.0.5:5060;lr;r2=on;did=2AXOPYHCFZJCPDGI>,<sip:mt@10.80.0.5:5080;lr;did=LMFV3M434C4WGM5L>,<sip:mo@10.80.0.5:5080;lr;did=PRWHK5F2LTQVRP77>,<sip:3iqp554vb5dijnjmjo2u3xbt2t@10.80.0.5:5060;lr;r2=on;did=W4LQBCOD7YORI5O7>,<sip:3iqp554vb5dijnjmjo2u3xbt2t@10.80.0.5:5063;transport=tcp;lr;r2=on;did=W4LQBCOD7YORI5O7;ue>
To: <sip:15550000008;phone-context=ims.mnc001.mcc999.3gppnetwork.org@ims.mnc001.mcc999.3gppnetwork.org;user=phone>;tag=100223009
From: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>;tag=14031060
Call-ID: o9voWSJ5T_Wr2ef6EsB2vg..@10.46.0.7
CSeq: 3 UPDATE
Session-Expires: 1800;refresher=uac
Content-Type: application/sdp
Content-Length: 460
Server: Core-Z5 N1982U.6.02.09.FR00
P-Access-Network-Info: 3GPP-NR-TDD;utran-cell-id-3gpp=9990100000100066C000
Contact: <sip:cbcd92dc-db6f-4aea-a207-e26b95efb9f2@10.46.0.8:41989>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: timer,precondition
Supported: 100rel,from-change,tdialog
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS

v=0
o=- 5 1001 IN IP4 10.46.0.8
s=QC VOIP
c=IN IP4 10.46.0.8
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50014 RTP/AVP 116 111
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:116 AMR-WB/16000/1
a=fmtp:116 mode-change-capability=2;max-red=0
a=rtpmap:111 telephone-event/16000
a=fmtp:111 0-15
a=curr:qos local sendrecv
a=curr:qos remote sendrecv
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
