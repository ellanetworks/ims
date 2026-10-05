SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.46.0.24:49165;branch=z9hG4bK3652a2a5976f12ef9899f8aa
From: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>;tag=ab18f589
To: <tel:15550000006;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=bRABJFaV9n
Call-ID: 862e30ad6cde66d6@10.46.0.24
CSeq: 3 UPDATE
Record-Route: <sip:f3o7bgwjgdlapb2kgsbgpxeghm@10.80.0.5:5063;did=RYBXHCUN44B7UCES;lr;r2=on;transport=tcp;ue>, <sip:f3o7bgwjgdlapb2kgsbgpxeghm@10.80.0.5:5060;did=RYBXHCUN44B7UCES;lr;r2=on>, <sip:mt@10.80.0.5:5080;did=4MURNPCV7TJ66XLJ;lr>, <sip:mo@10.80.0.5:5080;did=4EXJF5HUSGZY2TMH;lr>, <sip:kieypu6ghclrut4m5ob2zhojji@10.80.0.5:5060;did=TIVIL3N3DS2ATHZL;lr;r2=on>, <sip:kieypu6ghclrut4m5ob2zhojji@10.80.0.5:5063;did=TIVIL3N3DS2ATHZL;lr;r2=on;transport=tcp;ue>
Session-ID: 7004bf312be752b0a38251f15d339a70
Require: precondition,timer
Supported: 100rel,path,precondition,replaces,timer
Session-Expires: 1800;refresher=uac
Contact: <sip:10.46.0.25:63507>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
User-Agent: iOS/27.0 iPhone
Content-Type: application/sdp
Content-Length: 514

v=0
o=sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org 1791202047 1 IN IP4 10.46.0.25
s=-
c=IN IP4 10.46.0.25
b=AS:42
b=RS:0
b=RR:0
t=0 0
m=audio 49120 RTP/AVP 127 96
b=AS:42
b=RS:0
b=RR:0
a=rtpmap:127 EVS/16000
a=fmtp:127 br=5.9-24.4; bw=nb-wb; max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=ptime:20
a=maxptime:240
a=sendrecv
a=des:qos mandatory local sendrecv
a=curr:qos local none
a=des:qos mandatory remote sendrecv
a=curr:qos remote sendrecv
a=conf:qos remote sendrecv
