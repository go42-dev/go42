"""Supply live credentials and mutation targets without replacing invalid input."""

from copy import deepcopy
import uuid

import schemathesis

from fixtures import Fixtures


FIXTURES = Fixtures()
EXPECTED = {}
SCENARIOS = {}
EXPECTED_UUID = {}
PRESERVED = {}
DISPOSABLE = {}
OPERATIONS = {
    "signup", "login", "refresh", "logout", "users.me.read", "users.me.update",
    "users.list", "users.create", "users.get", "users.update", "users.delete",
}


def create_user(case):
    user = FIXTURES.create()
    DISPOSABLE.setdefault(case.id, []).append(user["uuid"])
    return user


def add_example(operation, examples, kwargs, status, name):
    case = operation.Case(**deepcopy(kwargs))
    examples.append(case)
    EXPECTED[case.id] = status
    SCENARIOS[case.id] = name
    identifier = operation.definition.raw["operationId"]
    with (FIXTURES.output / "planned.txt").open("a") as coverage:
        coverage.write(f"{identifier}:{name}\n")


@schemathesis.hook
def before_add_examples(ctx, examples):
    operation = ctx.operation
    identifier = operation.definition.raw["operationId"]
    if identifier not in OPERATIONS:
        raise AssertionError(f"Add successful contract fixtures for {identifier}")
    kwargs = {}
    if identifier in {"signup", "login", "users.create"}:
        kwargs["body"] = {"email": "fixture@example.invalid", "password": FIXTURES.password}
    elif identifier == "refresh":
        kwargs["body"] = {"token": "fixture"}
    elif identifier == "logout":
        kwargs["body"] = {"refresh_token": "fixture"}
    elif identifier in {"users.update", "users.me.update"}:
        kwargs["body"] = {"email": "fixture@example.invalid", "password": FIXTURES.password}
        if identifier == "users.me.update":
            kwargs["body"]["current_password"] = FIXTURES.password
    if "{uuid}" in operation.path:
        kwargs["path_parameters"] = {"uuid": str(uuid.uuid4())}
    if "body" in kwargs:
        kwargs["media_type"] = "application/json"

    # These live examples are also included in the native JUnit, HAR, and JSON reports.
    examples.clear()
    add_example(operation, examples, kwargs, 201 if identifier in {"signup", "users.create"} else 200, "success")
    if identifier.startswith("users."):
        add_example(operation, examples, kwargs, 401, "unauthenticated")
        denied = dict(kwargs)
        if identifier == "users.me.update":
            # A misconfigured privileged key must not change its owner's credentials.
            denied["body"] = {}
        add_example(operation, examples, denied, 403, "forbidden")
    if identifier == "login":
        for name in ("unknown-email", "wrong-password"):
            add_example(operation, examples, kwargs, 400, name)
    if identifier in {"signup", "users.create", "users.update", "users.me.update"}:
        for spelling in ("exact", "uppercase", "padded"):
            add_example(operation, examples, kwargs, 409, "conflict/" + spelling)
    if identifier == "users.me.update":
        for fields in (
            {"email": FIXTURES.email()}, {"password": FIXTURES.password + "!changed"},
            {"email": FIXTURES.email(), "password": FIXTURES.password + "!changed"},
        ):
            for proof in ("missing", "empty", "null", "wrong"):
                body = dict(fields)
                if proof != "missing":
                    body["current_password"] = {"empty": "", "null": None, "wrong": FIXTURES.password + "!wrong"}[proof]
                name = "proof/" + "+".join(fields) + "/" + proof
                add_example(operation, examples, dict(kwargs, body=body), 400, name)
        add_example(operation, examples, dict(kwargs, body={"email": "fixture"}), 400, "proof/same-email/missing")
        for name, body in (
            ("empty", {}), ("null-email", {"email": None}), ("null-password", {"password": None}),
            ("null-credentials", {"email": None, "password": None}),
            ("empty-proof", {"current_password": ""}), ("null-proof", {"current_password": None}),
            ("ignored-proof", {"current_password": "wrong"}),
            ("same-email", {"email": "fixture", "current_password": FIXTURES.password}),
        ):
            add_example(operation, examples, dict(kwargs, body=body), 200, "noop/" + name)
    if identifier in {"refresh", "logout"}:
        field = "token" if identifier == "refresh" else "refresh_token"
        for name, body, status in (
            ("missing", {}, 400), ("empty", {field: ""}, 400), ("null", {field: None}, 400),
            ("wrong-type", {field: 42}, 400), ("one-character", {field: "x"}, 401),
            ("whitespace", {field: " \t\n"}, 401), ("malformed", {field: "not-a-token"}, 401),
        ):
            add_example(operation, examples, dict(kwargs, body=body), status, "token/" + name)
        if identifier == "logout":
            for name, value in (("empty", ""), ("invalid", "not-an-access-token")):
                body = dict(kwargs["body"], access_token=value)
                add_example(operation, examples, dict(kwargs, body=body), 200, "legacy/" + name)
    if identifier == "users.list":
        for query, status in (
            ({"limit": 0}, 200), ({"limit": 100}, 200), ({"offset": 2147483647}, 200),
            ({"limit": -1}, 400), ({"limit": 101}, 400), ({"limit": ""}, 400),
            ({"limit": "invalid"}, 400), ({"limit": "1.5"}, 400),
            ({"offset": -1}, 400), ({"offset": 2147483648}, 400), ({"offset": ""}, 400),
        ):
            add_example(operation, examples, {"query": query}, status, "pagination/" + str(query))
    if identifier in {"users.get", "users.update", "users.delete"}:
        for value, status in (("not-a-uuid", 400), (str(uuid.uuid4()), 404)):
            add_example(operation, examples, dict(kwargs, path_parameters={"uuid": value}), status, "uuid/" + str(status))
    # Schemathesis executes explicit examples in reverse order. Give every operation
    # its successful fixture before spending its time slice on denial and boundary cases.
    examples.reverse()


@schemathesis.hook
def before_call(ctx, case, kwargs):
    identifier = case.operation.definition.raw["operationId"]
    scenario = SCENARIOS.get(case.id, "")
    positive = case.meta is None or case.meta.generation.mode.is_positive
    headers = case.headers if case.headers is not None else {}
    case.headers = headers
    if identifier.startswith("users."):
        headers["X-Api-Key"] = FIXTURES.key

    user = None
    if identifier in {"login", "refresh", "logout", "users.me.read"}:
        user = FIXTURES.existing()
    elif identifier == "users.me.update" and scenario != "forbidden":
        user = FIXTURES.existing() if scenario.startswith(("proof/", "noop/")) else create_user(case)

    tokens = None
    if identifier.startswith("users.me.") and scenario != "forbidden":
        headers.pop("X-Api-Key", None)
        tokens = FIXTURES.login(user)
        headers["Authorization"] = "Bearer " + tokens["access_token"]
        if identifier == "users.me.read":
            EXPECTED_UUID[case.id] = user["uuid"]

    if case.path_parameters and "uuid" in case.path_parameters and EXPECTED.get(case.id) != 404:
        try:
            uuid.UUID(str(case.path_parameters["uuid"]))
        except ValueError:
            pass  # Keep malformed UUIDs in generated negative requests.
        else:
            # Even a negative body must never update or delete an unowned identifier.
            target = create_user(case) if case.method in {"PUT", "DELETE"} else FIXTURES.existing()
            case.path_parameters["uuid"] = target["uuid"]
            if identifier == "users.get":
                EXPECTED_UUID[case.id] = target["uuid"]
            if identifier in {"users.update", "users.delete"}:
                user = target

    fixed_body = scenario.startswith(("proof/", "noop/", "token/"))
    if positive and isinstance(case.body, dict) and not fixed_body:
        if identifier == "login":
            case.body.update(email=user["email"], password=FIXTURES.password)
        elif identifier in {"refresh", "logout"}:
            tokens = FIXTURES.login(user)
            key = "token" if identifier == "refresh" else "refresh_token"
            case.body[key] = tokens["refresh_token"]
        else:
            if "email" in case.body and case.body["email"] is not None:
                case.body["email"] = FIXTURES.email()
            if "password" in case.body and case.body["password"] is not None:
                case.body["password"] = FIXTURES.password
            if identifier == "users.me.update" and any(case.body.get(field) is not None for field in ("email", "password")):
                case.body["current_password"] = FIXTURES.password

    if scenario == "unknown-email":
        case.body["email"] = FIXTURES.email()
    elif scenario == "wrong-password":
        case.body["password"] = FIXTURES.password + "!wrong"
    elif scenario in {"proof/same-email/missing", "noop/same-email"}:
        case.body["email"] = user["email"]
    elif scenario.startswith("conflict/"):
        existing = FIXTURES.existing()
        email = existing["email"]
        if scenario.endswith("uppercase"):
            email = email.upper()
        elif scenario.endswith("padded"):
            email = " \t" + email.upper() + " \n"
        case.body["email"] = email
        if identifier in {"users.update", "users.me.update"}:
            case.body["password"] = FIXTURES.password + "!changed"

    if scenario == "forbidden" and not identifier.startswith("users.me."):
        headers.pop("X-Api-Key", None)
        headers["Authorization"] = "Bearer " + FIXTURES.login(FIXTURES.existing())["access_token"]
    if user is not None and (
        scenario.startswith(("proof/", "noop/"))
        or identifier in {"users.update", "users.me.update"} and scenario.startswith("conflict/")
        or identifier in {"users.update", "users.delete"} and scenario == "forbidden"
    ):
        PRESERVED[case.id] = (user, tokens or FIXTURES.login(user))
    if scenario == "unauthenticated":
        case.headers.clear()


@schemathesis.hook
def after_call(ctx, case, response):
    identifier = case.operation.definition.raw["operationId"]
    if identifier in {"signup", "users.create"} and response.status_code == 201:
        created = response.json()["uuid"]
        FIXTURES.remember(created)
        DISPOSABLE.setdefault(case.id, []).append(created)
    expected = EXPECTED.get(case.id)
    if expected is not None and response.status_code != expected:
        raise AssertionError(f"{identifier}: expected {expected}, got {response.status_code}")
    if expected is not None and expected >= 400:
        content_types = response.headers.get("content-type", [])
        if not content_types or content_types[0].split(";")[0] != "application/problem+json":
            raise AssertionError(f"{identifier}: error response must use application/problem+json")
        if response.json().get("status") != expected:
            raise AssertionError(f"{identifier}: problem status does not match HTTP status")
    if response.status_code == 200 and case.id in EXPECTED_UUID:
        if response.json().get("uuid") != EXPECTED_UUID[case.id]:
            raise AssertionError(f"{identifier}: response did not identify the requested fixture")
    if case.id in PRESERVED:
        FIXTURES.assert_unchanged(*PRESERVED.pop(case.id))
    for identifier_to_delete in DISPOSABLE.pop(case.id, []):
        FIXTURES.discard(identifier_to_delete)
    scenario = SCENARIOS.get(case.id)
    if scenario is not None:
        with (FIXTURES.output / "scenarios.txt").open("a") as coverage:
            coverage.write(f"{identifier}:{scenario}\n")
    if scenario == "success":
        with (FIXTURES.output / "success.txt").open("a") as coverage:
            coverage.write(identifier + "\n")
