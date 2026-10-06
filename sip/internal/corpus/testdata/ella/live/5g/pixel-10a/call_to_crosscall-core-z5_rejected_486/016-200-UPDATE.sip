SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.46.0.3:49165;branch=z9hG4bK928d3c9a33de94a5e29b0f13
From: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>;tag=582401df
To: <tel:15550000005;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=103005316
Call-ID: 597b5d5e874d752d@10.46.0.3
CSeq: 3 UPDATE
Record-Route: <sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5063;transport=tcp;lr;r2=on;did=T7SHDVRJGHLDOKVN;ue>,<sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5060;lr;r2=on;did=T7SHDVRJGHLDOKVN>,<sip:mt@10.80.0.5:5080;lr;did=XERJQUZ6JZLW2PV7>,<sip:mo@10.80.0.5:5080;lr;did=3VYFCAB5PBCJ7SMI>,<sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5060;lr;r2=on;did=ICMM4EZXN4VRLZSX>,<sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5063;transport=tcp;lr;r2=on;did=ICMM4EZXN4VRLZSX;ue>
Content-Length: 458
Content-Type: application/sdp
Session-ID: 16f020d7f8d4504b9c6ef39d63c03de4
Server: Core-Z5 N1982U.6.02.09.FR00
Contact: <sip:b394eba0-c3ed-4e54-adf4-83d317ca2979@10.46.0.2:43300>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: timer,precondition
Supported: timer,100rel,from-change,tdialog
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Session-Expires: 1800;refresher=uac

v=0
o=- 10 1001 IN IP4 10.46.0.2
s=QC VOIP
c=IN IP4 10.46.0.2
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50056 RTP/AVP 100 96
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
