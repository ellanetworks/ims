NOTIFY sip:10.46.0.16:56059 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKQAGMXX2XPB5UWIFE3S34F4KRLB
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKDBQIBFPF5ESDTMK2CIX5Y4AP3Y
Record-Route: <sip:vseahg6apgnxmea47cvubdhxct@10.80.0.5:5063;lr;r2=on;ue>
Record-Route: <sip:vseahg6apgnxmea47cvubdhxct@10.80.0.5:5060;lr;r2=on>
Max-Forwards: 69
From: <sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org>;tag=P2IWRFJEQLYGKIJA3256LS7SEH
To: <sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org>;tag=8GWk4Q80C1
Call-ID: 9SvyIm5YAlAUC3wh5ZpFS5Pp
CSeq: 392697014 NOTIFY
Content-Length: 1649
Contact: <sip:10.80.0.5:5080>
Event: reg
Subscription-State: active;expires=600000
Content-Type: application/reginfo+xml

<?xml version="1.0" encoding="UTF-8"?>
<reginfo xmlns="urn:ietf:params:xml:ns:reginfo" version="0" state="full"><registration aor="sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org" id="r73697fd53368c350" state="active"><contact id="cedcddaf7f0199e5e" state="active" event="created" expires="3600" callid="UzMyDuSCJFHdwcWqjUtQI3Mr" cseq="2"><uri>sip:10.46.0.16:56059</uri><unknown-param name="+g.3gpp.accesstype">&#34;cellular2&#34;</unknown-param><unknown-param name="+g.3gpp.icsi-ref">&#34;urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel&#34;</unknown-param><unknown-param name="+g.3gpp.mid-call"></unknown-param><unknown-param name="+g.3gpp.smsip"></unknown-param><unknown-param name="+g.3gpp.srvcc-alerting"></unknown-param><unknown-param name="+sip.instance">&#34;&lt;urn:gsma:imei:00000000-000004-0&gt;&#34;</unknown-param><unknown-param name="audio"></unknown-param></contact></registration><registration aor="tel:+15550000006" id="r4d677cad82c7fd0d" state="active"><contact id="c92dbb85e51299ffd" state="active" event="created" expires="3600" callid="UzMyDuSCJFHdwcWqjUtQI3Mr" cseq="2"><uri>sip:10.46.0.16:56059</uri><unknown-param name="+g.3gpp.accesstype">&#34;cellular2&#34;</unknown-param><unknown-param name="+g.3gpp.icsi-ref">&#34;urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel&#34;</unknown-param><unknown-param name="+g.3gpp.mid-call"></unknown-param><unknown-param name="+g.3gpp.smsip"></unknown-param><unknown-param name="+g.3gpp.srvcc-alerting"></unknown-param><unknown-param name="+sip.instance">&#34;&lt;urn:gsma:imei:00000000-000004-0&gt;&#34;</unknown-param><unknown-param name="audio"></unknown-param></contact></registration></reginfo>