SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.0.0.1:5063;branch=z9hG4bKWEFZAJLRXEZIIYQAXWBU37CKOH
Via: SIP/2.0/UDP 10.0.0.1:5080;branch=z9hG4bK3VBCJ4VAUIOCMAOP2SXFZ64QHB
Via: SIP/2.0/UDP 10.0.0.1:5080;branch=z9hG4bK76P6HLIZHIRFOEJ22IUGQLIFBF
Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bKZ467D3BCOLGHRGZ767CT563NYE
Via: SIP/2.0/TCP 10.0.0.2:38873;branch=z9hG4bKYAM2QPB7QH2Y7U6BSATCOWPM7Q;rport=45067;received=10.0.0.2
Record-Route: <sip:mt@10.0.0.1:5080;lr;did=OMDLE7NNWM5OPZ5U>
Record-Route: <sip:mo@10.0.0.1:5080;lr;did=Y4MG2YEKOYNRRCID>
From: <sip:+15550001@ims.mnc001.mcc001.3gppnetwork.org>;tag=NF6P3SIPZFTE7O6C7FX4UYAMCO
To: <sip:+15550002@ims.mnc001.mcc001.3gppnetwork.org;user=phone>;tag=SUMK2J5KZ2HXSXFRXVKS5FQGKS
Call-ID: 459358e3-bf70-11f1-81ff-2bcf73babbc9@10.0.0.2
CSeq: 3 UPDATE
Content-Length: 435
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100010000001
Contact: <sip:4542e7f2-bf70-11f1-81ff-2bcf73babbc9@10.0.0.3:35047>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, PRACK, NOTIFY, MESSAGE
Content-Type: application/sdp
Require: precondition

v=0
o=- 117379067674630 1 IN IP4 10.0.0.3
s=-
c=IN IP4 10.0.0.3
t=0 0
m=audio 40004 RTP/AVP 116 111
b=AS:41
b=RS:512
b=RR:1537
a=rtpmap:116 AMR-WB/16000/1
a=fmtp:116 mode-change-capability=2;max-red=220
a=rtpmap:111 telephone-event/16000
a=fmtp:111 0-15
a=curr:qos local sendrecv
a=curr:qos remote sendrecv
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=ptime:20
a=maxptime:240
