# Backup

A backup holds the settings, users and call records. It leaves out the secrets and the API sessions. It leaves out the registrations, which each node keeps on its own.

Both paths require Admin. Backing up accepts API tokens. Restoring requires an access token: an API token gets `403`.

## Back Up

This path returns a backup of the database.

| Method | Path             |
| ------ | ---------------- |
| GET    | `/api/v1/backup` |

### Response

A gzipped tar archive, with `Content-Type: application/gzip` and its file name in `Content-Disposition`, such as `ims-backup-20261104T120000Z.tar.gz`.

| File            | Description                                        |
| --------------- | -------------------------------------------------- |
| `manifest.json` | Format, schema version and time of the backup.     |
| `SHA256SUMS`    | SHA-256 checksums of `ims.db` and `manifest.json`. |
| `ims.db`        | The database.                                      |

## Restore

This path replaces the settings, users and call records with those of a backup, on every node of a cluster. The cluster keeps its nodes and its secrets, and each node keeps its registrations.

| Method | Path              |
| ------ | ----------------- |
| POST   | `/api/v1/restore` |

### Request Body

An archive from [Back Up](#back-up), with `Content-Type: application/gzip`.

### Sample Response

```
{
    "result": {
        "message": "Backup restored successfully"
    }
}
```

### Response Codes

| Code  | Description                                                                                                                      |
| ----- | -------------------------------------------------------------------------------------------------------------------------------- |
| `400` | Not a backup, a checksum does not match, or the backup's schema is newer than this version's.                                    |
| `421` | The node is not the leader. See [Leader-Only Paths](https://docs.ellanetworks.com/ims/reference/api/cluster/#leader-only-paths). |
| `503` | No leader, or not ready to restore. Retry.                                                                                       |
