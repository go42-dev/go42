"""Owned HTTP fixtures shared by the runner and Schemathesis hooks."""

import os
from pathlib import Path
import time
import uuid

import requests


class Fixtures:
    def __init__(self):
        self.origin = os.environ["HTTP_SERVER_ADDRESS"].rstrip("/")
        self.base = self.origin + "/api/v1"
        self.key = os.environ["HTTP_API_KEY"]
        self.password = os.environ["CONTRACT_PASSWORD"]
        self.output = Path(os.environ["CONTRACT_OUTPUT"])
        self.client = requests.Session()
        self.client.trust_env = False
        self.user = None
        self.last_request = 0.0

    def request(self, method, path, *, expected, **kwargs):
        # Bound fixture setup and cleanup traffic as well as the CLI's test traffic.
        time.sleep(max(0, 0.1 - (time.monotonic() - self.last_request)))
        self.last_request = time.monotonic()
        response = self.client.request(
            method, self.base + path, timeout=10, allow_redirects=False, **kwargs
        )
        if response.status_code not in expected:
            # Response bodies may contain credentials; the native contract reports hold test failures.
            raise AssertionError(f"Fixture {method} {path}: expected {expected}, got {response.status_code}")
        return response

    @staticmethod
    def email():
        return f"contract-{uuid.uuid4().hex}@example.invalid"

    def remember(self, identifier):
        identifier = str(uuid.UUID(identifier))
        if identifier == str(uuid.UUID(int=0)):
            raise AssertionError("A create response returned the bootstrap administrator UUID")
        with (self.output / "fixtures.txt").open("a") as journal:
            journal.write(identifier + "\n")

    def create(self):
        response = self.request(
            "POST", "/users", expected=(201,), headers={"X-Api-Key": self.key},
            json={"email": self.email(), "password": self.password},
        )
        user = response.json()
        self.remember(user["uuid"])
        return user

    def existing(self):
        if self.user is None:
            self.user = self.create()
        return self.user

    def login(self, user):
        response = self.request(
            "POST", "/auth/login", expected=(200,),
            json={"email": user["email"], "password": self.password},
        )
        tokens = response.json()
        if not tokens.get("access_token") or not tokens.get("refresh_token"):
            raise AssertionError("Login did not return both session tokens")
        return tokens

    def assert_unchanged(self, user, tokens):
        current = self.request(
            "GET", "/users/me", expected=(200,), headers={"Authorization": "Bearer " + tokens["access_token"]},
        ).json()
        if current["uuid"] != user["uuid"]:
            raise AssertionError("Existing session changed its owner")
        if current["email"] != user["email"]:
            raise AssertionError("Rejected or no-op update changed the user's email")
        self.login(user)  # The original password must still authenticate.
        self.request("POST", "/auth/refresh", expected=(200,), json={"token": tokens["refresh_token"]})

    def discard(self, identifier):
        journal = self.output / "fixtures.txt"
        identifiers = journal.read_text().splitlines()
        if identifier not in identifiers:
            raise AssertionError("Cannot delete an unowned fixture")
        self.request("DELETE", "/users/" + identifier, expected=(200, 404), headers={"X-Api-Key": self.key})
        self.request("GET", "/users/" + identifier, expected=(404,), headers={"X-Api-Key": self.key})
        self._write_journal(value for value in identifiers if value != identifier)

    def _write_journal(self, identifiers):
        journal = self.output / "fixtures.txt"
        pending = journal.with_suffix(".tmp")
        pending.write_text("".join(value + "\n" for value in identifiers))
        pending.replace(journal)

    def cleanup(self):
        journal = self.output / "fixtures.txt"
        remaining = []
        identifiers = set(journal.read_text().splitlines()) if journal.exists() else set()
        deadline = time.monotonic() + 60
        for identifier in sorted(identifiers):
            if time.monotonic() >= deadline:
                remaining.append(identifier)
                continue
            try:
                self.request(
                    "DELETE", f"/users/{identifier}", expected=(200, 404),
                    headers={"X-Api-Key": self.key},
                )
                self.request(
                    "GET", f"/users/{identifier}", expected=(404,),
                    headers={"X-Api-Key": self.key},
                )
            except (AssertionError, requests.RequestException):
                remaining.append(identifier)
        self._write_journal(remaining)
        self.client.close()
        if remaining:
            raise AssertionError(f"Cleanup failed for {len(remaining)} users; see {journal}")
        print(f"Cleanup: {len(identifiers)} owned users removed or already deleted.", flush=True)
