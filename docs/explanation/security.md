---
description: How Ella IMS authenticates phones, protects their signalling, decides whom to trust, and controls who can use its UI and API.
---

# Security

## Authenticating phones

Phones authenticate with the keys on their SIM, with IMS-AKA. When a phone registers, the S-CSCF fetches an authentication vector from the HSS, challenges the phone, and checks its answer. The phone checks the network in turn, so each side proves itself to the other.

## Protecting signalling with IPsec

The same authentication produces keys that the phone and the P-CSCF use to set up IPsec between them. From then on, the phone's SIP messages travel in ESP on dedicated ports. This protects the signalling from tampering, and ties every request to the phone that authenticated.

Ella IMS sets up these IPsec associations in the Linux kernel. That's why it needs `CAP_NET_ADMIN`, and why ESP must reach it through any firewall.

## The trust domain

SIP nodes tell each other who a request is from, for example with the `P-Asserted-Identity` header. A node believes this only from the nodes it trusts: its trust domain.

Phones are never trusted: the P-CSCF takes a phone's identity from its authentication.

Besides Ella IMS itself, the I-CSCF and the S-CSCF trust the networks in `sip.trusted_networks`, such as those of Application Servers. Trust goes by source address, so protect these networks, and keep the phones out of them.

## SIP trunks

Carriers are outside the trust domain, unless a trunk is marked trusted. Ella IMS knows a carrier by the source address of its requests, and ignores requests from any other address.

Only subscribers' calls leave for a SIP trunk. Application Servers can't send calls to a trunk directly.

Little crosses the border. Information internal to the IMS, such as the phone's location, never leaves. Header fields that could make a phone answer by itself or steer a call never come in. Only a trusted carrier can assert the caller's number.

With TLS, Ella IMS checks the carrier's certificate on every connection it opens. A trunk with `tls.required` also needs a certificate on the connections the carrier opens, for mutual authentication. Without it, a connection the carrier opens is authenticated by its source address only.

## Users of the UI and API

Everyone who uses the UI or the API is a user with a role. See [Roles](../reference/api/index.md#roles). Programs, such as a monitoring system, use an API token instead of a password.

Users can also log in through the organization's identity provider. See [Single sign-on](single_sign_on.md).

Until Ella IMS is initialized, anyone who reaches the UI can make themselves Admin, so initialize it right after installing.

## HTTPS

Ella IMS serves the UI and the API over plain HTTP unless it is given a certificate in `api.tls` (see the [configuration file](../reference/config_file.md)). Use HTTPS whenever the UI or the API is reachable over a network you do not control.
