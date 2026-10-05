NOTIFY sip:10.46.0.18:65006 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKD3NYHZACTBQSAPRZHE34PBDQ4Q
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKHVDC7JZNNKZBTAC5AX7JN4NLNK
Record-Route: <sip:yc5wf6oncq2vusfgsof6ihohf4@10.80.0.5:5063;lr;r2=on;ue>
Record-Route: <sip:yc5wf6oncq2vusfgsof6ihohf4@10.80.0.5:5060;lr;r2=on>
Max-Forwards: 69
From: <sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org>;tag=NMTON7U7YM654HLIXUFNTWDWQ7
To: <sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org>;tag=OYydV8vFjq
Call-ID: 8cSVSgCYrMpfjV4wfob0tiz1
CSeq: 79029421 NOTIFY
Content-Length: 1649
Contact: <sip:10.80.0.5:5080>
Event: reg
Subscription-State: active;expires=600000
Content-Type: application/reginfo+xml

<?xml version="1.0" encoding="UTF-8"?>
<reginfo xmlns="urn:ietf:params:xml:ns:reginfo" version="0" state="full"><registration aor="sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org" id="r8644babb9cd86c45" state="active"><contact id="c9516112ff5fdba22" state="active" event="created" expires="3600" callid="TANy8spWzCOaV9CNalFUtERF" cseq="5"><uri>sip:10.46.0.18:65006</uri><unknown-param name="+g.3gpp.accesstype">&#34;cellular2&#34;</unknown-param><unknown-param name="+g.3gpp.icsi-ref">&#34;urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel&#34;</unknown-param><unknown-param name="+g.3gpp.mid-call"></unknown-param><unknown-param name="+g.3gpp.smsip"></unknown-param><unknown-param name="+g.3gpp.srvcc-alerting"></unknown-param><unknown-param name="+sip.instance">&#34;&lt;urn:gsma:imei:00000000-000005-0&gt;&#34;</unknown-param><unknown-param name="audio"></unknown-param></contact></registration><registration aor="tel:+15550000003" id="r6c2a38b9e29539dc" state="active"><contact id="c2f8f3aea0990cdea" state="active" event="created" expires="3600" callid="TANy8spWzCOaV9CNalFUtERF" cseq="5"><uri>sip:10.46.0.18:65006</uri><unknown-param name="+g.3gpp.accesstype">&#34;cellular2&#34;</unknown-param><unknown-param name="+g.3gpp.icsi-ref">&#34;urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel&#34;</unknown-param><unknown-param name="+g.3gpp.mid-call"></unknown-param><unknown-param name="+g.3gpp.smsip"></unknown-param><unknown-param name="+g.3gpp.srvcc-alerting"></unknown-param><unknown-param name="+sip.instance">&#34;&lt;urn:gsma:imei:00000000-000005-0&gt;&#34;</unknown-param><unknown-param name="audio"></unknown-param></contact></registration></reginfo>