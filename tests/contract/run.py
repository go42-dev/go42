"""Run the pinned Schemathesis CLI against a dedicated, already-running application."""

import base64
import fcntl
import importlib.metadata
import json
import os
from pathlib import Path
import platform
import re
import secrets
import subprocess
import sys
import tempfile
import time
import tomllib
from urllib.parse import urlsplit

import requests
import yaml

from fixtures import Fixtures


ROOT = Path(__file__).resolve().parents[2]
OUTPUT = ROOT / ".build/contract"
CONFIG = ROOT / "etc/schemathesis.toml"
SCHEMA = ROOT / "api/openapi/v1/.combined.yaml"
EXCLUDED = {("/dummy", "get")}  # Redocly placeholder; no application handler.
HTTP_METHODS = {"get", "post", "put", "patch", "delete", "head", "options", "trace"}
DEFAULT_API_KEY = "api_kXqdf2uQ7hmOARp-pZrhA6_IsZSeKCmSEM4YFKBGIzA"  # Public bootstrap test credential.


def sanitize(value):
    """Native reports redact headers, but their JSON and HAR bodies also need redaction."""
    if isinstance(value, dict):
        result = {}
        for key, item in value.items():
            if any(part in key.lower() for part in ("password", "token", "secret")):
                result[key] = "[Filtered]"
            elif key == "$base64" and isinstance(item, str):
                payload = base64.b64decode(item).decode("utf-8", errors="replace")
                result[key] = base64.b64encode(sanitize(payload).encode()).decode()
            else:
                result[key] = sanitize(item)
        return result
    if isinstance(value, list):
        return [sanitize(item) for item in value]
    if isinstance(value, str):
        try:
            nested = json.loads(value)
        except (ValueError, RecursionError):
            nested = None
        if isinstance(nested, (dict, list)):
            return json.dumps(sanitize(nested), ensure_ascii=True)
        for secret in (os.environ.get("HTTP_API_KEY"), os.environ.get("CONTRACT_PASSWORD")):
            if secret:
                value = value.replace(secret, "[Filtered]")
        return re.sub(r"eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+", "[Filtered]", value)
    return value


def preflight(fixtures):
    deadline = time.monotonic() + 30
    while True:
        try:
            response = fixtures.client.get(fixtures.origin + "/ready", timeout=3, allow_redirects=False)
            if response.status_code == 200:
                break
        except requests.RequestException:
            pass
        if time.monotonic() >= deadline:
            raise AssertionError("Application did not become ready within 30 seconds")
        time.sleep(0.2)
    for path in ("/health", "/metrics"):
        response = fixtures.client.get(fixtures.origin + path, timeout=3, allow_redirects=False)
        if response.status_code != 200:
            raise AssertionError(f"{path}: expected 200, got {response.status_code}")
        if path == "/metrics" and "application_build{" not in response.text:
            raise AssertionError("/metrics did not return Prometheus metrics")
    fixtures.request("GET", "/users", expected=(200,), headers={"X-Api-Key": fixtures.key})
    # The privileged fixture key must lack self-service permissions for denial cases.
    fixtures.request("GET", "/users/me", expected=(403,), headers={"X-Api-Key": fixtures.key})
    fixtures.request("PUT", "/users/me", expected=(403,), headers={"X-Api-Key": fixtures.key}, json={})


def sanitize_reports(output):
    incomplete = []
    for name in ("report.json", "requests.har"):
        path = output / name
        if path.exists():
            try:
                path.write_text(json.dumps(sanitize(json.loads(path.read_text())), ensure_ascii=True) + "\n")
            except (ValueError, RecursionError):
                # A killed writer may leave partial JSON. Never publish its unredacted contents.
                path.unlink()
                incomplete.append(name)
    path = output / "junit.xml"
    if path.exists():
        path.write_text(sanitize(path.read_text()))
    if incomplete:
        raise AssertionError(f"Removed incomplete reports: {', '.join(incomplete)}")


def run_suite():
    origin = os.environ.get("HTTP_SERVER_ADDRESS", "http://localhost:8080").rstrip("/")
    url = urlsplit(origin)
    if url.scheme not in {"http", "https"} or not url.netloc or url.path or url.query or url.fragment or url.username:
        raise ValueError("HTTP_SERVER_ADDRESS must be an origin without credentials or /api/v1")
    OUTPUT.mkdir(parents=True, exist_ok=True)
    if (OUTPUT / "fixtures.txt").exists() and (OUTPUT / "fixtures.txt").read_text().strip():
        raise ValueError("Previous cleanup is incomplete; resolve .build/contract/fixtures.txt before running again")
    for name in ("success.txt", "planned.txt", "scenarios.txt", "junit.xml", "requests.har", "report.json", "schemathesis.log"):
        (OUTPUT / name).unlink(missing_ok=True)
    os.environ.update(
        HTTP_SERVER_ADDRESS=origin, CONTRACT_OUTPUT=str(OUTPUT),
        HTTP_API_KEY=os.environ.get("HTTP_API_KEY") or DEFAULT_API_KEY,
        CONTRACT_PASSWORD=secrets.token_urlsafe(36),
    )
    raw = yaml.safe_load(SCHEMA.read_text())
    expected = {
        operation["operationId"] for path, methods in raw["paths"].items()
        for method, operation in methods.items()
        if method in HTTP_METHODS and (path, method) not in EXCLUDED
    }
    if not expected:
        raise AssertionError("No implemented OpenAPI operations found")
    config = tomllib.loads(CONFIG.read_text())
    print(
        f"Schemathesis {importlib.metadata.version('schemathesis')}; seed {config['seed']}; "
        f"{len(expected)} operations; {config['max-time']}s generated-test budget.", flush=True,
    )
    print(f"Python {platform.python_version()}; Hypothesis {importlib.metadata.version('hypothesis')}", flush=True)
    fixtures = Fixtures()
    result = 1
    try:
        preflight(fixtures)
        env = dict(os.environ, PYTHONPATH=str(Path(__file__).parent), NO_PROXY="*", no_proxy="*")
        for name in tuple(env):
            if name.lower() in {"http_proxy", "https_proxy", "all_proxy"}:
                del env[name]
        command = [
            str(Path(sys.executable).parent / "schemathesis"), "--config-file", str(CONFIG),
            "run", str(SCHEMA), "--url", origin + "/api/v1", "--exclude-path", "/dummy",
        ]
        # Raw output remains outside the uploaded directory until credentials are removed.
        with tempfile.TemporaryFile(mode="w+") as log:
            try:
                completed = subprocess.run(
                    command, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT,
                    timeout=config["max-time"] + 60,
                )
                result = completed.returncode
            finally:
                log.seek(0)
                text = sanitize(log.read())
                (OUTPUT / "schemathesis.log").write_text(text)
                print(text, flush=True)
        covered = set((OUTPUT / "success.txt").read_text().splitlines()) if (OUTPUT / "success.txt").exists() else set()
        if expected != covered:
            raise AssertionError(f"Missing successful contract scenarios: {sorted(expected - covered)}")
        planned = set((OUTPUT / "planned.txt").read_text().splitlines())
        scenarios = set((OUTPUT / "scenarios.txt").read_text().splitlines())
        if not planned or planned != scenarios:
            raise AssertionError(f"Missing deterministic contract scenarios: {sorted(planned - scenarios)}")
        report = json.loads((OUTPUT / "report.json").read_text())
        if not report["complete"] or report["operations"]["tested"] != len(expected):
            raise AssertionError("Schemathesis did not complete every selected operation")
        preflight(fixtures)
        print(
            f"Successful scenarios: {len(covered)}/{len(expected)} operations; "
            f"{len(scenarios)} deterministic scenarios. Runtime probes passed.", flush=True,
        )
    finally:
        try:
            fixtures.cleanup()
        finally:
            sanitize_reports(OUTPUT)
    return result


def main():
    OUTPUT.parent.mkdir(parents=True, exist_ok=True)
    with (OUTPUT.parent / "contract.lock").open("w") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise RuntimeError("Another contract run is using .build/contract") from None
        return run_suite()


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as error:
        print(f"Contract suite failed: {sanitize(str(error))}", file=sys.stderr)
        sys.exit(1)
