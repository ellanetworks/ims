---
description: How Ella IMS authenticates phones, protects their signalling, and decides whom to trust.
---

# Security

## Authenticating phones

Phones authenticate with the keys on their SIM, with IMS-AKA, a variant of the AKA the core network uses at attach. When a phone registers, the S-CSCF fetches an authentication vector from the HSS, challenges the phone, and checks its answer. The phone checks the network in turn, so each side proves itself to the other. No password is configured on the phone or in Ella IMS.

## Protecting signalling with IPsec

The same authentication produces keys that the phone and the P-CSCF use to set up IPsec between them. From then on, the phone's SIP messages travel in ESP on dedicated ports. This protects the signalling from tampering, and ties every request to the phone that authenticated.

Ella IMS sets up these IPsec associations in the Linux kernel. That's why it needs `CAP_NET_ADMIN`, and why ESP must reach it through any firewall.

## The trust domain

SIP nodes tell each other who a request is from, for example with the P-Asserted-Identity header. A node only believes such claims from nodes it trusts: its trust domain.

Phones are never in the trust domain: the P-CSCF takes their identity from their authentication, not from what they claim. The P-CSCF trusts only Ella IMS's own addresses.

The I-CSCF and the S-CSCF also trust the networks in `sip.trusted_networks`, where other nodes such as Application Servers live. Trust goes by source address, so these networks must be protected by the operator, and must never include the phones. For the same reason, Ella IMS rejects a trusted network that covers every IPv4 or every IPv6 address.
