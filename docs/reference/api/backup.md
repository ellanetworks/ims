---
description: RESTful API reference for backing up Ella IMS.
---

# Backup

This path requires Admin. API tokens are accepted.

## Back Up

This path returns a backup of the database.

| Method | Path             |
| ------ | ---------------- |
| GET    | `/api/v1/backup` |

### Response

A gzipped tar archive, with `Content-Type: application/gzip` and its file name in `Content-Disposition`, such as `ims-backup-20261104T120000Z.tar.gz`.
