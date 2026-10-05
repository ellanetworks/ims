NOTIFY sip:15550000007@10.46.0.2:6400 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bK7JRMONGHK7PCDTN6TKM7LTHGF4
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKBAF2DF3WTEA6DAWQOMWNLIHCYH
Record-Route: <sip:5zdnqhmykiyb4kz7gkmqhsd4pk@10.80.0.5:5063;lr;r2=on;ue>
Record-Route: <sip:5zdnqhmykiyb4kz7gkmqhsd4pk@10.80.0.5:5060;lr;r2=on>
Max-Forwards: 69
From: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>;tag=JHLBU3KHV2J7JHLINUKCFSED2Z
To: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>;tag=6a038016
Call-ID: am-R1q6C4oM-Zpsmbic-_w..@10.46.0.2
CSeq: 715365327 NOTIFY
Content-Length: 1241
Contact: <sip:10.80.0.5:5080>
Event: reg
Subscription-State: active;expires=600000
Content-Type: application/reginfo+xml

<?xml version="1.0" encoding="UTF-8"?>
<reginfo xmlns="urn:ietf:params:xml:ns:reginfo" version="0" state="full"><registration aor="sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org" id="r9379751fcb28f06a" state="active"><contact id="c1d709233b8dbc87e" state="active" event="created" expires="3600" callid="Z2DA--Y-Vm1l6HrBSerjCw..@10.46.0.2" cseq="2" q="1.0"><uri>sip:999010000000007@10.46.0.2:6400</uri><unknown-param name="+sip.instance">&#34;&lt;urn:gsma:imei:00000000-000007-0&gt;&#34;</unknown-param><unknown-param name="+g.3gpp.icsi-ref">&#34;urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel&#34;</unknown-param><unknown-param name="+g.3gpp.smsip"></unknown-param></contact></registration><registration aor="tel:+15550000007" id="r1aaeb792187383dd" state="active"><contact id="c0dc9277aae19fddf" state="active" event="created" expires="3600" callid="Z2DA--Y-Vm1l6HrBSerjCw..@10.46.0.2" cseq="2" q="1.0"><uri>sip:999010000000007@10.46.0.2:6400</uri><unknown-param name="+sip.instance">&#34;&lt;urn:gsma:imei:00000000-000007-0&gt;&#34;</unknown-param><unknown-param name="+g.3gpp.icsi-ref">&#34;urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel&#34;</unknown-param><unknown-param name="+g.3gpp.smsip"></unknown-param></contact></registration></reginfo>