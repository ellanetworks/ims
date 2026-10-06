SIP/2.0 200 OK
From: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=103310767
To: <tel:15550000002;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=b3bbc289
Contact: <sip:15550000002@10.46.0.3:49165>;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video
Call-ID: 103310760_1760534736@10.46.0.2
CSeq: 103310762 UPDATE
Via: SIP/2.0/TCP 10.46.0.2:43300;branch=z9hG4bK99169867
Content-Length: 455
Allow: INVITE,CANCEL,UPDATE,PRACK,BYE,ACK,MESSAGE,SUBSCRIBE,NOTIFY,PUBLISH,REFER,INFO,OPTIONS
Content-Type: application/sdp
Server: Google_Pixel10a_Android 16_BD6A.251031.001.A4
Require: precondition

v=0
o=sip:IMS_RCS-USER 12345682 12345680 IN IP4 10.46.0.3
s=VOLTECALL
c=IN IP4 10.46.0.3
t=0 0
m=audio 7010 RTP/AVP 104 96
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-change-capability=2; max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=ptime:20
a=maxptime:240
a=sendrecv
a=curr:qos local sendrecv
a=curr:qos remote sendrecv
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
