---
description: How Ella IMS's built-in MCPTT server fits in the IMS.
---

# Push-to-Talk

Mission Critical Push To Talk (MCPTT) is the 3GPP standard for group calls with floor control: one member talks at a time, and the others listen. It runs as an Application Server on top of the IMS.

## A server inside the IMS

Ella IMS includes an MCPTT server, so push-to-talk needs no separate node. It runs in the same process as the CSCFs, which reach it in memory rather than over the network.

Like any Application Server, it learns about its users through the IMS. When an MCPTT user's phone registers, the S-CSCF tells the MCPTT server, because the user's filter criteria in the HSS say so. This is why Ella Core's MCPTT setup adds the **MCPTT** filter criteria to the users' profile.

## Naming

The MCPTT server's name is `sip:mcptt.<home domain>`, and the home domain comes from the operator's MCC and MNC. Ella IMS routes every request for that host to the server, so the HSS needs no Public Service Identity for it. Changing the MCC or MNC renames the server.

## Users and groups

The HSS knows subscribers. The MCPTT server knows MCPTT users, identified by an MCPTT ID such as `alice`, and the groups they belong to. Each MCPTT user maps to the IMS identities of their subscriber, which is how the server ties a registered phone to a person.

## Media

Unlike a regular call, a group call's voice goes through Ella IMS: its media relay receives each talker's audio and copies it to every listener. Floor control, deciding who may talk, runs on a separate control channel. That's why the MCPTT server has its own media ports.
