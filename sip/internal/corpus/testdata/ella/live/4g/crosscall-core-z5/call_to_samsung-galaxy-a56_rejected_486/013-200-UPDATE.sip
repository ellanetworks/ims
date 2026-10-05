SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bK4KIXAO4D2OSE6K4GI5FAJ72LQT
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKSB53YHDLBQN7YB63TSVFMEITM5
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK2TV56NBCUXLUMILUIU6PF5CKRB
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKXZ7GZ7ICZZRK5EYKAC33DSNINP
Via: SIP/2.0/TCP 10.46.0.10:44417;branch=z9hG4bK3381559394
Max-Forwards: 70
Record-Route: <sip:xecajs2762idoehm7nsyclb72l@10.80.0.5:5063;transport=tcp;lr;r2=on;did=RECGJJSC6J6YKNJ7;ue>
Record-Route: <sip:xecajs2762idoehm7nsyclb72l@10.80.0.5:5060;lr;r2=on;did=RECGJJSC6J6YKNJ7>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=3ENOBYBBU7EFN3OG>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=44AXFMFDT2AQ3R54>
Record-Route: <sip:hgmaj5sms7v54nx74sze7jdv7u@10.80.0.5:5060;lr;r2=on;did=5YSDZ32Z5G3ZWLQ3>
Record-Route: <sip:hgmaj5sms7v54nx74sze7jdv7u@10.80.0.5:5063;transport=tcp;lr;r2=on;did=5YSDZ32Z5G3ZWLQ3;ue>
Require: precondition
Contact: <sip:15550000007@10.46.0.5:6100>;+sip.instance="<urn:gsma:imei:00000000-000007-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
To: <tel:15550000007;phone-context=ims.mnc001.mcc999.3gppnetwork.org>;tag=e623af5f
From: <sip:15550000008@ims.mnc001.mcc999.3gppnetwork.org>;tag=6943448
Call-ID: 6943441_3070962960@10.46.0.10
CSeq: 6943443 UPDATE
Session-Expires: 1800;refresher=uas
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Content-Type: application/sdp
Supported: timer, 100rel, precondition
User-Agent: SM-A566W-A566WVLS6BZB7 Samsung IMS 6.0
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=9990100010019b01
Content-Length: 446

v=0
o=SAMSUNG-IMS-UE 92792587720 1 IN IP4 10.46.0.5
s=SS VOIP
c=IN IP4 10.46.0.5
t=0 0
m=audio 1254 RTP/AVP 104 96
b=AS:41
b=RS:512
b=RR:1537
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=ptime:20
a=maxptime:240
a=sendrecv
a=curr:qos local sendrecv
a=curr:qos remote sendrecv
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
