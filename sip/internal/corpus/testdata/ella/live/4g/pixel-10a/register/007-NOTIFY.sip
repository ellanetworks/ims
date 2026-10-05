NOTIFY sip:45a6a166-7a26-94f7-8e36-9e4dd6bdef7c@10.46.0.23:49167 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKGJD2IRF7TYZPKDUJSKTD45LUIV
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKFCDB27X2CQFFSZLKCVNSRGO6YH
Record-Route: <sip:i6kmfnpitfyf3ajs4ggorrbp5h@10.80.0.5:5063;transport=tcp;lr;r2=on;ue>
Record-Route: <sip:i6kmfnpitfyf3ajs4ggorrbp5h@10.80.0.5:5060;lr;r2=on>
Max-Forwards: 69
From: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>;tag=FH52PK3PEH2JZCSBGSQDXSEPL5
To: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>;tag=dabae0ed
Call-ID: 607d0cbf9e163710@10.46.0.23
CSeq: 1268273822 NOTIFY
Content-Length: 1793
Contact: <sip:10.80.0.5:5080>
Event: reg
Subscription-State: active;expires=600000
Content-Type: application/reginfo+xml

<?xml version="1.0" encoding="UTF-8"?>
<reginfo xmlns="urn:ietf:params:xml:ns:reginfo" version="0" state="full"><registration aor="sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org" id="r88b3a8b22a382499" state="active"><contact id="c88c441d29331e241" state="active" event="created" expires="3600" callid="4e61f18221ea2db@10.46.0.23" cseq="2"><uri>sip:45a6a166-7a26-94f7-8e36-9e4dd6bdef7c@10.46.0.23:49167</uri><unknown-param name="audio"></unknown-param><unknown-param name="+g.3gpp.nw-init-ussi"></unknown-param><unknown-param name="+g.3gpp.icsi-ref">&#34;urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel&#34;</unknown-param><unknown-param name="video"></unknown-param><unknown-param name="+g.3gpp.accesstype">&#34;cellular2&#34;</unknown-param><unknown-param name="+g.3gpp.smsip"></unknown-param><unknown-param name="reg-id">1</unknown-param><unknown-param name="+sip.instance">&#34;&lt;urn:gsma:imei:00000000-000006-0&gt;&#34;</unknown-param></contact></registration><registration aor="tel:+15550000002" id="rd1fd47c2132d2ef8" state="active"><contact id="c0c8556da2e09c023" state="active" event="created" expires="3600" callid="4e61f18221ea2db@10.46.0.23" cseq="2"><uri>sip:45a6a166-7a26-94f7-8e36-9e4dd6bdef7c@10.46.0.23:49167</uri><unknown-param name="audio"></unknown-param><unknown-param name="+g.3gpp.nw-init-ussi"></unknown-param><unknown-param name="+g.3gpp.icsi-ref">&#34;urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel&#34;</unknown-param><unknown-param name="video"></unknown-param><unknown-param name="+g.3gpp.accesstype">&#34;cellular2&#34;</unknown-param><unknown-param name="+g.3gpp.smsip"></unknown-param><unknown-param name="reg-id">1</unknown-param><unknown-param name="+sip.instance">&#34;&lt;urn:gsma:imei:00000000-000006-0&gt;&#34;</unknown-param></contact></registration></reginfo>