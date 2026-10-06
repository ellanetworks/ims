NOTIFY sip:a87a2e9a-4c32-439f-afc0-825ea2f8b236@10.46.0.9:43885 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKOBYAKFLV5CNMULQLNHIFUUSV2P
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK44UNLC4RWAPEDON7VSHTVF65HQ
Record-Route: <sip:nsbgwhufa64gwwi7ngbk7jry6d@10.80.0.5:5063;lr;r2=on;ue>
Record-Route: <sip:nsbgwhufa64gwwi7ngbk7jry6d@10.80.0.5:5060;lr;r2=on>
Max-Forwards: 69
From: <sip:15550000008@ims.mnc001.mcc001.3gppnetwork.org>;tag=LND4WYHC3CKD6QZ2X7UPU2YWVD
To: <sip:15550000008@ims.mnc001.mcc001.3gppnetwork.org>;tag=110267866
Call-ID: 110267864_4109522880@10.46.0.9
CSeq: 138525445 NOTIFY
Content-Length: 1369
Contact: <sip:10.80.0.5:5080>
Event: reg
Subscription-State: active;expires=600000
Content-Type: application/reginfo+xml

<?xml version="1.0" encoding="UTF-8"?>
<reginfo xmlns="urn:ietf:params:xml:ns:reginfo" version="0" state="full"><registration aor="sip:15550000008@ims.mnc001.mcc001.3gppnetwork.org" id="r7892f9d30b387a9e" state="active"><contact id="c568591b08251e770" state="active" event="created" expires="3600" callid="110267676_4111793664@10.46.0.9" cseq="110267677"><uri>sip:a87a2e9a-4c32-439f-afc0-825ea2f8b236@10.46.0.9:43885</uri><unknown-param name="+g.3gpp.accesstype">&#34;cellular2&#34;</unknown-param><unknown-param name="+sip.instance">&#34;&lt;urn:gsma:imei:00000000-000001-0&gt;&#34;</unknown-param><unknown-param name="+g.3gpp.ps-data-off">&#34;inactive&#34;</unknown-param><unknown-param name="+g.3gpp.smsip"></unknown-param></contact></registration><registration aor="tel:+15550000008" id="r7076471236b28715" state="active"><contact id="c04500cb6bf108a22" state="active" event="created" expires="3600" callid="110267676_4111793664@10.46.0.9" cseq="110267677"><uri>sip:a87a2e9a-4c32-439f-afc0-825ea2f8b236@10.46.0.9:43885</uri><unknown-param name="+g.3gpp.accesstype">&#34;cellular2&#34;</unknown-param><unknown-param name="+sip.instance">&#34;&lt;urn:gsma:imei:00000000-000001-0&gt;&#34;</unknown-param><unknown-param name="+g.3gpp.ps-data-off">&#34;inactive&#34;</unknown-param><unknown-param name="+g.3gpp.smsip"></unknown-param></contact></registration></reginfo>