---
description: Step-by-step instructions to set the home network domain of Ella IMS.
---

# Set the Home Network Domain

This guide sets the home network domain of Ella IMS to a domain other than the one derived from the MCC and MNC. The examples use `ims.example.com`.

## Prerequisites

- **HSS**: an HSS whose private and public user identities use `ims.example.com`.
- **ISIM**: SIMs whose ISIM holds `ims.example.com` as the home network domain, and identities in it.

## 1. Set the domain

In the Ella IMS UI, go to the **Operator** page. Click the edit icon next to **Home Network Domain**, enter `ims.example.com` in **Domain**, and click **Update**.

## 2. Check the domain

Under **Home Network Domain**, **Domain** shows `ims.example.com`, and **Also Accepted** shows the domain derived from the MCC and MNC.
