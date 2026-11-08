---
description: RESTful API reference for backing up Ella IMS.
---

# Backup

A backup is a copy of the database. It holds:

- the users, their single sign-on links and their API tokens;
- the settings: the operator, the Diameter peers and routes, the policy, the SIP trunks and routes, the IP-SM-GW number, the call record retention and single sign-on, its client secret included;
- the MCPTT service configuration, users and groups;
- the call records.

It leaves out:

- the key that signs access tokens and the single sign-on cookie key;
- the login sessions of the UI;
- the registrations of phones and what they carry: the bindings, the user profiles from the HSS, the subscriptions to registration state, the P-CSCF flows and their IPsec security associations;
- the MCPTT registrations and affiliations, and the IP-SM-GW registrations.

This path requires Admin. An API token can back up.

## Back Up

This path returns a backup of the database.

| Method | Path             |
| ------ | ---------------- |
| GET    | `/api/v1/backup` |

### Response

A gzipped tar archive, with `Content-Type: application/gzip` and its file name in `Content-Disposition`, such as `ims-backup-20261104T120000Z.tar.gz`.

| File            | Description |
| --------------- | ----------- |
| `manifest.json` | Format, schema version and time of the backup. |
| `SHA256SUMS`    | SHA-256 checksums of `ims.db` and `manifest.json`. |
| `ims.db`        | The database. |
