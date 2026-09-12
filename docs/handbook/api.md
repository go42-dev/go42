---
id: api
title: Using the API
sidebar_position: 7
---

# Using the API

This guide describes the implemented authentication and user interfaces. Use it to verify a local instance or diagnose
a failed request. Follow [feature implementation](development.md#implementing-a-feature) when changing the interface.

## Contracts and authentication

HTTP business routes use `/api/v1`; probes and metrics are at the root. The
[authentication OpenAPI source](../../api/openapi/v1/auth.yaml) and [Protobuf source](../../api/proto/auth/v1/auth.proto)
describe the contracts. HTTP handlers and error mappings are handwritten. The bundled `dummy.yaml` contract is a tooling
placeholder and has no application handler.

| Interface | Authentication and authorization |
| --- | --- |
| HTTP `/api/v1/auth/*` | Signup/login use credentials; refresh/logout use a refresh token in the body |
| HTTP `/api/v1/users/*` | `Authorization: Bearer ACCESS_TOKEN` or `X-API-Key`; each route also checks its permission |
| gRPC user methods | `x-api-key` metadata when authorization is enabled; each method needs its registered permissions |

A normal signed-up user has self-service permissions and cannot list or administer other users. Use `/users/me` to verify
that user's access. The inherited administrative test key is intended for administrative methods and does not grant
`users:read_self`. gRPC does not use the HTTP login JWT as its authentication credential.

Permission enforcement is defined in the [HTTP adapter](../../internal/auth/adapters/http/v1/adapter.go),
[HTTP authentication middleware](../../internal/auth/middleware/auth.go), and
[gRPC authentication and access interceptors](../../internal/auth/interceptors).

## Session sequence

All paths below have the `/api/v1` prefix. Send JSON request bodies with `Content-Type: application/json`.

| Request | Input | Expected successful result |
| --- | --- | --- |
| `POST /auth/signup` | `email`, `password` | `201` user record; creates the account |
| `POST /auth/login` | `email`, `password` | `200` with `access_token`, `refresh_token`, and `expires_in` |
| `GET /users/me` | Bearer access token | `200` with the authenticated user's UUID, roles, and permissions |
| `POST /auth/refresh` | `token`: the current refresh token | `200` with a new token pair |
| `POST /auth/logout` | `refresh_token`: the current refresh token | Empty `200`; ends the session |

Use the newly returned token pair after refresh. Refresh tokens are single-use; reusing one revokes its session, including
successor tokens. A retry with an old refresh token is not a validity check. After logout, access and refresh using that
session are rejected. The [authentication service](../../internal/auth/auth.go) owns these transitions.

Signup currently returns a model without reloaded role associations, so its response can contain empty roles and permissions
even though the `user` role was assigned. Read `/users/me` after login to inspect the persisted user's permissions.

## Check an authenticated request locally

Use a dedicated local test instance with the inherited self-service role configured. The following Python 3 check needs
no additional packages. It creates a disposable account, logs in, verifies read-self, and logs out. Credentials and tokens
remain in memory. The account remains in the test database; a default in-memory instance discards it on restart.

```sh
python3 - <<'PY'
import json
import secrets
import urllib.request

base = "http://127.0.0.1:8080/api/v1"
client = urllib.request.build_opener(urllib.request.ProxyHandler({}))

def call(method, path, body=None, token=None):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    data = json.dumps(body).encode() if body is not None else None
    request = urllib.request.Request(base + path, data=data, headers=headers, method=method)
    with client.open(request, timeout=10) as response:
        content = response.read()
        return response.status, json.loads(content) if content else None

credentials = {
    "email": "docs-" + secrets.token_hex(12) + "@example.invalid",
    "password": secrets.token_urlsafe(32),
}
status, user = call("POST", "/auth/signup", credentials)
assert status == 201
status, tokens = call("POST", "/auth/login", credentials)
assert status == 200
try:
    status, current = call("GET", "/users/me", token=tokens["access_token"])
    assert status == 200 and current["uuid"] == user["uuid"]
finally:
    status, _ = call("POST", "/auth/logout", {"refresh_token": tokens["refresh_token"]})
    assert status == 200
print("PASS: signup, login, read-self, and logout")
PY
```

Change the loopback port if your test instance uses another listener. A failed HTTP request stops the check; inspect its
status with the table below. This verifies one local session flow, not administrative access, external backends, or load.

## Diagnose HTTP failures

Application errors use `application/problem+json` with `type`, `title`, and `status`; validation can add an `errors` list.
The current renderer puts the request URI in `type`. Capture the status, route, time, and request ID without credentials
or tokens. Compare with the [HTTP error mapping](../../internal/auth/adapters/http/v1/errors.go).

| Status | Investigate |
| --- | --- |
| `400` | Invalid input, pagination, email/password rules, or rejected login credentials |
| `401` | Missing/invalid access credentials, expired or revoked session, or invalid/reused refresh token |
| `403` | Recognized credentials lack the route's permission; an ordinary user cannot use administrative routes |
| `404` | Incorrect route/prefix or missing resource; distinguish routing from an entity lookup |
| `409` | User email already exists |
| `429` | Authentication or transport rate limit; inspect the configured limiter and recent request volume |
| `503` | Authentication dependency unavailable; inspect database/cache errors and readiness |
| `500` | Unhandled server error; correlate request ID and component logs |

The OpenAPI login response currently advertises `403` for an inactive user, while `Service.Login` returns
`ErrInvalidCredentials` and the HTTP adapter renders `400`. This contract/implementation discrepancy remains to be
resolved in a focused API change; do not infer the intended requirement from either status alone.

For gRPC, inspect the returned status rather than HTTP codes: missing credentials map to `Unauthenticated`, missing
permissions to `PermissionDenied`, and authentication dependency failures to `Unavailable`. The
[gRPC error mapping](../../internal/auth/adapters/grpc/v1/errors.go) covers service errors. Use
[operations](operations.md#failure-investigation-and-recovery) for evidence collection and recovery checks.
