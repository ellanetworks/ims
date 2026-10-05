UPDATE sip:15550000007@10.46.0.5:6100 SIP/2.0
Max-Forwards: 70
To: <tel:15550000007;phone-context=ims.mnc001.mcc999.3gppnetwork.org>;tag=e623af5f
From: <sip:15550000008@ims.mnc001.mcc999.3gppnetwork.org>;tag=6943448
Call-ID: 6943441_3070962960@10.46.0.10
CSeq: 6943443 UPDATE
Content-Length: 456
Via: SIP/2.0/TCP 10.46.0.10:44417;branch=z9hG4bK3381559394
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=9990100010019B01
Security-Verify: ipsec-3gpp;q=0.1;prot=esp;mod=trans;spi-c=117444731;spi-s=130227876;port-c=5064;port-s=5063;alg=hmac-sha-1-96;ealg=null
Route: <sip:hgmaj5sms7v54nx74sze7jdv7u@10.80.0.5:5063;transport=tcp;lr;r2=on;did=5YSDZ32Z5G3ZWLQ3;ue>,<sip:hgmaj5sms7v54nx74sze7jdv7u@10.80.0.5:5060;lr;r2=on;did=5YSDZ32Z5G3ZWLQ3>,<sip:mo@10.80.0.5:5080;lr;did=44AXFMFDT2AQ3R54>,<sip:mt@10.80.0.5:5080;lr;did=3ENOBYBBU7EFN3OG>,<sip:xecajs2762idoehm7nsyclb72l@10.80.0.5:5060;lr;r2=on;did=RECGJJSC6J6YKNJ7>,<sip:xecajs2762idoehm7nsyclb72l@10.80.0.5:5063;transport=tcp;lr;r2=on;did=RECGJJSC6J6YKNJ7;ue>
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Type: application/sdp
Require: sec-agree,precondition
Proxy-Require: sec-agree
Contact: <sip:8476f6db-5c6a-4256-a0dd-69db67bd85bd@10.46.0.10:44417>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi

v=0
o=- 10 1001 IN IP4 10.46.0.10
s=QC VOIP
c=IN IP4 10.46.0.10
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50042 RTP/AVP 104 96
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
