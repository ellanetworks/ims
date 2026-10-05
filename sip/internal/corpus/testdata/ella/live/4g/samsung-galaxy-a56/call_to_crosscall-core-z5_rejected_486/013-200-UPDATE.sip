SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKC2S6RUB57XF4LYLAZAM632JKP5,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKJWQCLYMNKGWUGBQ6Q3KP3SB45C,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKMXJVVEBEDY43TPPEFCGQLZHN2E,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKZXFSW5BW2KSQHNGK6BQDF5ODDU,SIP/2.0/TCP 10.46.0.5:6100;branch=z9hG4bK-524287-1---6df789f57247cb9c;rport=6101;transport=TCP;received=10.46.0.5
Record-Route: <sip:hgmaj5sms7v54nx74sze7jdv7u@10.80.0.5:5063;transport=tcp;lr;r2=on;did=NGLDXIEZ5OALAYA7;ue>,<sip:hgmaj5sms7v54nx74sze7jdv7u@10.80.0.5:5060;lr;r2=on;did=NGLDXIEZ5OALAYA7>,<sip:mt@10.80.0.5:5080;lr;did=KEXY5BRVIXVY4EYD>,<sip:mo@10.80.0.5:5080;lr;did=XW2GSNKCROYJZMJF>,<sip:xecajs2762idoehm7nsyclb72l@10.80.0.5:5060;lr;r2=on;did=LRN2CU4SKJFAP5AG>,<sip:xecajs2762idoehm7nsyclb72l@10.80.0.5:5063;transport=tcp;lr;r2=on;did=LRN2CU4SKJFAP5AG;ue>
To: <sip:15550000008;phone-context=ims.mnc001.mcc999.3gppnetwork.org@ims.mnc001.mcc999.3gppnetwork.org;user=phone>;tag=6286761
From: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>;tag=1c84d201
Call-ID: bml0BhpQjnxykIBoUSLWdg..@10.46.0.5
CSeq: 3 UPDATE
Session-Expires: 1800;refresher=uac
Content-Type: application/sdp
Content-Length: 462
Server: Core-Z5 N1982U.6.02.09.FR00
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=9990100010019B01
Contact: <sip:8476f6db-5c6a-4256-a0dd-69db67bd85bd@10.46.0.10:44417>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: timer,precondition
Supported: 100rel,from-change,tdialog
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS

v=0
o=- 4 1001 IN IP4 10.46.0.10
s=QC VOIP
c=IN IP4 10.46.0.10
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50044 RTP/AVP 116 111
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
