UPDATE sip:15550000002@10.46.0.24:49165 SIP/2.0
Require: precondition,sec-agree
Supported: 100rel,path,replaces,timer
To: <tel:+15550000002>;tag=10f68f23
Route: <sip:f3o7bgwjgdlapb2kgsbgpxeghm@10.80.0.5:5063;did=HWFAUINCWZVHSK4D;lr;r2=on;transport=tcp;ue>, <sip:f3o7bgwjgdlapb2kgsbgpxeghm@10.80.0.5:5060;did=HWFAUINCWZVHSK4D;lr;r2=on>, <sip:mo@10.80.0.5:5080;did=KACYVEHWOAQWQUXS;lr>, <sip:mt@10.80.0.5:5080;did=NW5S4KKIPFQWPQUA;lr>, <sip:kieypu6ghclrut4m5ob2zhojji@10.80.0.5:5060;did=5FM5CGBUFV4OODSU;lr;ob;r2=on>, <sip:kieypu6ghclrut4m5ob2zhojji@10.80.0.5:5063;did=5FM5CGBUFV4OODSU;lr;ob;r2=on;transport=tcp;ue>
From: <sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org>;tag=N9VFxKYIi7
Call-ID: Vp5lvr2fZa3rj8x005nYYEPM
Session-ID: 81ea83e819dd815b4cc760f421299969
Contact: <sip:10.46.0.25:63507>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
CSeq: 3 UPDATE
Via: SIP/2.0/TCP 10.46.0.25:63507;branch=z9hG4bKQSXk1mWJJ2c00qJ;rport
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
Max-Forwards: 70
User-Agent: iOS/27.0 iPhone
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015ee0000
Security-Verify: ipsec-3gpp;alg=hmac-sha-1-96;ealg=null;mod=trans;port-c=5064;port-s=5063;prot=esp;q=0.1;spi-c=8430301;spi-s=114600604
Proxy-Require: sec-agree
Content-Type: application/sdp
Content-Length: 496

v=0
o=sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org 1791203043 1791203044 IN IP4 10.46.0.25
s=-
c=IN IP4 10.46.0.25
b=AS:30
b=RS:0
b=RR:0
t=0 0
m=audio 49120 RTP/AVP 116 105
b=AS:30
b=RS:0
b=RR:0
a=rtpmap:116 EVS/16000
a=fmtp:116 br=13.2; bw=nb-wb; max-red=220
a=rtpmap:105 telephone-event/16000
a=fmtp:105 0-15
a=ptime:20
a=maxptime:240
a=sendrecv
a=des:qos mandatory local sendrecv
a=curr:qos local sendrecv
a=des:qos mandatory remote sendrecv
a=curr:qos remote none
