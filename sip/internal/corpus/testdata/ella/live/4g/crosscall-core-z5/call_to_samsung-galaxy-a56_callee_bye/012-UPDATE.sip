UPDATE sip:15550000007@10.46.0.5:6100 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKKQF3RFB4HANLR4KPL7M6JBTES5
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKP254HB6CUMLLF5MGIYF3XUYRFP
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKDAQEQXORTNGOCHEEFVMDB6HILH
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bK7UHRFARG24KHDJI2VFUFDFWOCD
Max-Forwards: 66
To: <tel:15550000007;phone-context=ims.mnc001.mcc999.3gppnetwork.org>;tag=65496c41
From: <sip:15550000008@ims.mnc001.mcc999.3gppnetwork.org>;tag=6695658
Call-ID: 6695652_3070641088@10.46.0.10
CSeq: 6695654 UPDATE
Content-Length: 455
Via: SIP/2.0/TCP 10.46.0.10:44417;branch=z9hG4bK35778520
Record-Route: <sip:xecajs2762idoehm7nsyclb72l@10.80.0.5:5063;transport=tcp;lr;r2=on;did=T6ASTG76EWMGTP4J;ue>
Record-Route: <sip:xecajs2762idoehm7nsyclb72l@10.80.0.5:5060;lr;r2=on;did=T6ASTG76EWMGTP4J>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=HXRZLRZ4VCLXZYMC>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=HREBAURAXEW2Z6G7>
Record-Route: <sip:hgmaj5sms7v54nx74sze7jdv7u@10.80.0.5:5060;lr;r2=on;did=NARA3RU62ALLI6BX>
Record-Route: <sip:hgmaj5sms7v54nx74sze7jdv7u@10.80.0.5:5063;transport=tcp;lr;r2=on;did=NARA3RU62ALLI6BX;ue>
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Type: application/sdp
Contact: <sip:8476f6db-5c6a-4256-a0dd-69db67bd85bd@10.46.0.10:44417>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: precondition

v=0
o=- 8 1001 IN IP4 10.46.0.10
s=QC VOIP
c=IN IP4 10.46.0.10
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50028 RTP/AVP 104 96
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
