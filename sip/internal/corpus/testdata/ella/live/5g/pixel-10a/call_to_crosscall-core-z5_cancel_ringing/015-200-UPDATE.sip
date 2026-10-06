SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.46.0.3:49165;branch=z9hG4bKf033add0987f60a7d2b8f6eb
From: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>;tag=f3fba2c9
To: <tel:15550000005;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=103140307
Call-ID: 328a0d67c51df36c@10.46.0.3
CSeq: 3 UPDATE
Record-Route: <sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5063;transport=tcp;lr;r2=on;did=LANHD46HRCZAIJHW;ue>,<sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5060;lr;r2=on;did=LANHD46HRCZAIJHW>,<sip:mt@10.80.0.5:5080;lr;did=CKZF4B4Q4KNTJFU3>,<sip:mo@10.80.0.5:5080;lr;did=MDAACB6W7KGDQU5I>,<sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5060;lr;r2=on;did=QQPRLPCU4YQ4EQ6B>,<sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5063;transport=tcp;lr;r2=on;did=QQPRLPCU4YQ4EQ6B;ue>
Content-Length: 458
Content-Type: application/sdp
Session-ID: 9bc41936e65d546181bf36c36d83457b
Server: Core-Z5 N1982U.6.02.09.FR00
Contact: <sip:b394eba0-c3ed-4e54-adf4-83d317ca2979@10.46.0.2:43300>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: timer,precondition
Supported: timer,100rel,from-change,tdialog
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Session-Expires: 1800;refresher=uac

v=0
o=- 11 1001 IN IP4 10.46.0.2
s=QC VOIP
c=IN IP4 10.46.0.2
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50018 RTP/AVP 100 96
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:100 AMR-WB/16000/1
a=fmtp:100 mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=curr:qos local sendrecv
a=curr:qos remote sendrecv
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
