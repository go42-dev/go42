"""Failure-path checks for test-owned resources and credential-bearing reports."""

import base64
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import requests

from fixtures import Fixtures
from run import sanitize, sanitize_reports


class RunnerTests(unittest.TestCase):
    def test_redacts_credentials_in_native_report_bodies_and_reproduction_commands(self):
        body = json.dumps({"access_token": "session-secret", "email": "test@example.invalid"})
        report = {
            "request": {"body": {"$base64": base64.b64encode(body.encode()).decode()}},
            "response": {"content": {"text": body}},
            "command": "curl -H 'X-Api-Key:fixture-key' -d '{\"password\":\"fixture-password\"}'",
        }
        with patch.dict(os.environ, HTTP_API_KEY="fixture-key", CONTRACT_PASSWORD="fixture-password"):
            result = sanitize(report)
        decoded = base64.b64decode(result["request"]["body"]["$base64"]).decode()
        self.assertNotIn("session-secret", decoded)
        self.assertIn("test@example.invalid", decoded)
        rendered = json.dumps(result)
        for secret in ("session-secret", "fixture-key", "fixture-password"):
            self.assertNotIn(secret, rendered)

    def test_cleanup_retains_failed_users_and_verifies_successful_deletion(self):
        first = "11111111-1111-4111-8111-111111111111"
        second = "22222222-2222-4222-8222-222222222222"
        with tempfile.TemporaryDirectory() as output, patch.dict(os.environ, {
            "HTTP_SERVER_ADDRESS": "http://localhost:8080", "HTTP_API_KEY": "fixture-key",
            "CONTRACT_PASSWORD": "fixture-password", "CONTRACT_OUTPUT": output,
        }):
            fixture = Fixtures()
            fixture.remember(first)
            fixture.remember(second)
            with patch.object(fixture, "request", side_effect=[None, None, requests.Timeout()]) as request:
                with self.assertRaisesRegex(AssertionError, "Cleanup failed for 1 users"):
                    fixture.cleanup()
            self.assertEqual(request.call_args_list[1].args, ("GET", f"/users/{first}"))
            self.assertEqual((Path(output) / "fixtures.txt").read_text(), second + "\n")

    def test_preserves_native_multiline_json_report_structure(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            report = {"complete": True, "operations": {"tested": 11}}
            (output / "report.json").write_text(json.dumps(report, indent=2))
            sanitize_reports(output)
            self.assertEqual(json.loads((output / "report.json").read_text()), report)

    def test_discard_forgets_only_verified_deletions_and_refuses_unowned_users(self):
        identifier = "11111111-1111-4111-8111-111111111111"
        with tempfile.TemporaryDirectory() as output, patch.dict(os.environ, {
            "HTTP_SERVER_ADDRESS": "http://localhost:8080", "HTTP_API_KEY": "fixture-key",
            "CONTRACT_PASSWORD": "fixture-password", "CONTRACT_OUTPUT": output,
        }):
            fixture = Fixtures()
            self.addCleanup(fixture.client.close)
            fixture.remember(identifier)
            journal = Path(output) / "fixtures.txt"
            with patch.object(fixture, "request", side_effect=[None, requests.Timeout()]):
                with self.assertRaises(requests.Timeout):
                    fixture.discard(identifier)
            self.assertEqual(journal.read_text(), identifier + "\n")
            with patch.object(fixture, "request") as request:
                fixture.discard(identifier)
                self.assertEqual(request.call_args_list[1].args, ("GET", "/users/" + identifier))
                self.assertEqual(journal.read_text(), "")
                request.reset_mock()
                with self.assertRaisesRegex(AssertionError, "unowned fixture"):
                    fixture.discard(identifier)
                request.assert_not_called()

    def test_incomplete_reports_fail_and_cannot_be_uploaded_with_credentials(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            (output / "requests.har").write_text('{"password": "test-secret",')
            (output / "report.json").write_text('{"access_token": "test-secret"}')
            with self.assertRaisesRegex(AssertionError, "Removed incomplete reports: requests.har"):
                sanitize_reports(output)
            self.assertFalse((output / "requests.har").exists())
            self.assertNotIn("test-secret", (output / "report.json").read_text())


if __name__ == "__main__":
    unittest.main()
