UPDATE sip:10.46.0.25:63507 SIP/2.0
From: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>;tag=5dbd15fa
To: <tel:15550000006;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=4OowVpBWzJ
Contact: <sip:15550000002@10.46.0.24:49165>;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video
Call-ID: 2fe61b022ddfe082@10.46.0.24
CSeq: 3 UPDATE
Max-Forwards: 70
Via: SIP/2.0/TCP 10.46.0.24:49165;branch=z9hG4bKe1c8afb798dee70138e9bd44
Content-Length: 442
Content-Type: application/sdp
Require: precondition,sec-agree
Supported: 100rel,199,histinfo,precondition,sec-agree,timer
Allow: INVITE,CANCEL,UPDATE,PRACK,BYE,ACK,MESSAGE,SUBSCRIBE,NOTIFY,PUBLISH,REFER,INFO,OPTIONS
Proxy-Require: sec-agree
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100010019b01
User-Agent: Google_Pixel10a_Android 16_BD6A.251031.001.A4
Route: <sip:kieypu6ghclrut4m5ob2zhojji@10.80.0.5:5063;transport=tcp;lr;did=IDI2ZWJH2JYXERQO;r2=on;ue>,<sip:kieypu6ghclrut4m5ob2zhojji@10.80.0.5:5060;lr;did=IDI2ZWJH2JYXERQO;r2=on>,<sip:mo@10.80.0.5:5080;lr;did=NDT3AW34I34W4GUJ>,<sip:mt@10.80.0.5:5080;lr;did=GPUBPJIVDZ5BJCZ4>,<sip:f3o7bgwjgdlapb2kgsbgpxeghm@10.80.0.5:5060;lr;did=BP4ZIIWE7C4BAQTB;r2=on>,<sip:f3o7bgwjgdlapb2kgsbgpxeghm@10.80.0.5:5063;transport=tcp;lr;did=BP4ZIIWE7C4BAQTB;r2=on;ue>
Session-ID: 7f855f9310a1594980f338b8521adda3
Security-Verify: ipsec-3gpp;q=0.1;alg=hmac-sha-1-96;prot=esp;mod=trans;ealg=null;spi-c=259322710;spi-s=69633574;port-c=5064;port-s=5063

v=0
o=sip:IMS_RCS-USER 12345679 12345679 IN IP4 10.46.0.24
s=VOLTECALL
c=IN IP4 10.46.0.24
t=0 0
m=audio 7010 RTP/AVP 127 96
b=AS:42
b=RS:0
b=RR:0
a=rtpmap:127 EVS/16000/1
a=fmtp:127 max-red=0; br=5.9-24.4; bw=nb-wb
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=ptime:20
a=maxptime:240
a=sendrecv
a=curr:qos local sendrecv
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
