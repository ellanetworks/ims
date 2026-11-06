# Initialize

## Initialize Ella IMS

This path creates the first user, an Admin, and logs it in.

| Method | Path           |
| ------ | -------------- |
| POST   | `/api/v1/init` |

### Request Body

| Field      | Type   | Required | Description    |
| ---------- | ------ | -------- | -------------- |
| `email`    | string | Yes      | Email address. |
| `password` | string | Yes      | 8 to 72 bytes. |

### Response Codes

| Code  | Description                                |
| ----- | ------------------------------------------ |
| `201` | Created.                                   |
| `403` | A user exists.                             |
| `429` | Too many attempts from the client address. |

### Sample Request

```
{
    "email": "admin@example.com",
    "password": "correct horse"
}
```

### Sample Response

```
{
    "result": {
        "message": "System initialized successfully",
        "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."
    }
}
```
