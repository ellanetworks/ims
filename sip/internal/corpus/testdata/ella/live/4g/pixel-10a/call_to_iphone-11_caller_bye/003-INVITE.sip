INVITE sip:10.46.0.25:63507 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKD4NXODPFM3353ZZQOWSVBHGYPI
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKPBIWESNKA2V7Z6SBTUUAFHBZAL
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKW244VG5HZGWLEFHA3G6AW5LYGD
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKQBT2NYC5W6VE7QSQEKTHSA33SU
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKGSGL2BLMJF42NYICJ6ZI5MLZK6
From: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>;tag=ab18f589
To: <tel:15550000006;phone-context=ims.mnc001.mcc001.3gppnetwork.org>
Contact: <sip:15550000002@10.46.0.24:49165>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
Call-ID: 862e30ad6cde66d6@10.46.0.24
CSeq: 1 INVITE
Max-Forwards: 65
Via: SIP/2.0/TCP 10.46.0.24:49165;branch=z9hG4bK920c56c3fd6211a6bb7dafa8
Record-Route: <sip:f3o7bgwjgdlapb2kgsbgpxeghm@10.80.0.5:5063;transport=tcp;lr;r2=on;did=RYBXHCUN44B7UCES;ue>
Record-Route: <sip:f3o7bgwjgdlapb2kgsbgpxeghm@10.80.0.5:5060;lr;r2=on;did=RYBXHCUN44B7UCES>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=4MURNPCV7TJ66XLJ>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=4EXJF5HUSGZY2TMH>
Record-Route: <sip:kieypu6ghclrut4m5ob2zhojji@10.80.0.5:5060;lr;r2=on;did=TIVIL3N3DS2ATHZL>
Record-Route: <sip:kieypu6ghclrut4m5ob2zhojji@10.80.0.5:5063;transport=tcp;lr;r2=on;did=TIVIL3N3DS2ATHZL;ue>
Content-Length: 839
Session-Expires: 1800;refresher=uac
Min-SE: 90
Allow: INVITE,CANCEL,UPDATE,PRACK,BYE,ACK,MESSAGE,SUBSCRIBE,NOTIFY,PUBLISH,REFER,INFO,OPTIONS
Content-Type: application/sdp
User-Agent: Google_Pixel10a_Android 16_BD6A.251031.001.A4
Supported: 100rel,199,histinfo,precondition,sec-agree,timer
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";video
Session-ID: 7004bf312be752b0a38251f15d339a70
Accept: application/sdp,application/3gpp-ims+xml
P-Called-Party-ID: <tel:+15550000006>
P-Early-Media: supported
P-Asserted-Identity: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550000002>

v=0
o=sip:IMS_RCS-USER 12345678 12345678 IN IP4 10.46.0.24
s=VOLTECALL
c=IN IP4 10.46.0.24
t=0 0
m=audio 7010 RTP/AVP 127 100 105 98 103 96 97
b=AS:42
b=RS:600
b=RR:2000
a=rtpmap:127 EVS/16000/1
a=fmtp:127 max-red=0; br=5.9-24.4; bw=nb-swb
a=rtpmap:100 AMR-WB/16000/1
a=fmtp:100 mode-change-capability=2; max-red=0
a=rtpmap:105 AMR-WB/16000/1
a=fmtp:105 octet-align=1; mode-change-capability=2; max-red=0
a=rtpmap:98 AMR/8000/1
a=fmtp:98 mode-change-capability=2; max-red=0
a=rtpmap:103 AMR/8000/1
a=fmtp:103 octet-align=1; mode-change-capability=2; max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=rtpmap:97 telephone-event/8000
a=fmtp:97 0-15
a=ptime:20
a=maxptime:240
a=sendrecv
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos optional remote sendrecv
