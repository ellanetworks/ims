---
description: RESTful API reference for managing Ella IMS.
---

# API

## Authentication

The API does not authenticate requests.

## Responses

Ella IMS's API responses are JSON objects. A successful response carries a `result`:

```json
{
    "result": "Result content"
}
```

A failed response carries an `error`:

```json
{
    "error": "Error message"
}
```

`GET /api/v1/metrics` returns text in the [Prometheus exposition format](https://prometheus.io/docs/instrumenting/exposition_formats/#text-format-details), `GET /api/v1/openapi.yaml` returns YAML, and unknown paths and methods answer in plain text.

## OpenAPI Specification

The full API is described by an [OpenAPI 3.1](https://spec.openapis.org/oas/v3.1.0) specification embedded in the binary. Fetch it from any running Ella IMS instance:

```
GET /api/v1/openapi.yaml
```

## Status codes

- 200 - Success.
- 201 - Created.
- 202 - Accepted.
- 400 - Bad request.
- 404 - Not found.
- 405 - Method not allowed.
- 409 - Conflict.
- 500 - Internal server error.
- 503 - Service unavailable.
