"""Keep deliberate contract failures intact while supplying real fixture identities."""

from copy import deepcopy
from pathlib import Path
from tempfile import TemporaryDirectory
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

import schemathesis

with patch("fixtures.Fixtures"):
    import hooks


class HookTests(unittest.TestCase):
    def setUp(self):
        output = TemporaryDirectory()
        self.addCleanup(output.cleanup)
        self.fixture = Mock(
            output=Path(output.name), password="Correct!password123", key="privileged-key",
        )
        self.fixture.existing.return_value = {"uuid": "11111111-1111-4111-8111-111111111111", "email": "existing@example.com"}
        self.fixture.create.return_value = {"uuid": "22222222-2222-4222-8222-222222222222", "email": "target@example.com"}
        self.fixture.login.return_value = {"access_token": "ordinary-jwt", "refresh_token": "refresh-token"}
        self.fixture.email.return_value = "replacement@example.com"
        patcher = patch.object(hooks, "FIXTURES", self.fixture)
        patcher.start()
        self.addCleanup(patcher.stop)
        for state in (hooks.EXPECTED, hooks.SCENARIOS, hooks.EXPECTED_UUID, hooks.PRESERVED, hooks.DISPOSABLE):
            state.clear()
        self.schema = schemathesis.openapi.from_path(Path(__file__).resolve().parents[2] / "api/openapi/v1/auth.yaml")

    def examples(self, path, method):
        context = SimpleNamespace(operation=self.schema[path][method])
        cases = []
        hooks.before_add_examples(context, cases)
        return cases

    def test_explicit_missing_and_invalid_proof_and_tokens_are_not_repaired(self):
        for path, method in (("/users/me", "PUT"), ("/auth/refresh", "POST"), ("/auth/logout", "POST")):
            for case in self.examples(path, method):
                name = hooks.SCENARIOS[case.id]
                if not name.startswith(("proof/", "noop/", "token/")) or "same-email" in name:
                    continue
                with self.subTest(path=path, name=name):
                    original = deepcopy(case.body)
                    hooks.before_call(None, case, {})
                    self.assertEqual(original, case.body)

    def test_generated_negative_body_and_positive_null_fields_are_preserved(self):
        operation = self.schema["/users/me"]["PUT"]
        for body, positive in (({"email": "new@example.com"}, False), ({"email": None, "password": None}, True)):
            case = SimpleNamespace(
                operation=operation, id="generated", body=deepcopy(body), headers={}, path_parameters={},
                meta=SimpleNamespace(generation=SimpleNamespace(mode=SimpleNamespace(is_positive=positive))),
            )
            hooks.before_call(None, case, {})
            self.assertEqual(body, case.body)

    def test_denial_cases_use_only_the_intended_credential(self):
        for path, method in (("/users", "POST"), ("/users/me", "GET"), ("/users/me", "PUT")):
            case = next(case for case in self.examples(path, method) if hooks.SCENARIOS[case.id] == "forbidden")
            hooks.before_call(None, case, {})
            if path == "/users/me":
                self.assertEqual({"X-Api-Key": "privileged-key"}, dict(case.headers))
                if method == "PUT":
                    self.assertEqual({}, case.body)
            else:
                self.assertEqual({"Authorization": "Bearer ordinary-jwt"}, dict(case.headers))

    def test_conflicts_use_another_users_email_and_preserve_original_session(self):
        for path in ("/users/{uuid}", "/users/me"):
            for case in self.examples(path, "PUT"):
                if not hooks.SCENARIOS[case.id].startswith("conflict/"):
                    continue
                hooks.before_call(None, case, {})
                self.assertEqual("existing@example.com", case.body["email"].strip().lower())
                self.assertNotEqual(self.fixture.password, case.body["password"])
                user, tokens = hooks.PRESERVED[case.id]
                self.assertEqual("target@example.com", user["email"])
                self.assertEqual(self.fixture.login.return_value, tokens)

    def test_problem_responses_match_content_type_and_http_status(self):
        case = next(case for case in self.examples("/users", "POST") if hooks.SCENARIOS[case.id] == "forbidden")
        response = Mock(status_code=403, headers={"content-type": ["application/problem+json"]})
        response.json.return_value = {"type": "/api/v1/users", "title": "Forbidden", "status": 403}
        hooks.after_call(None, case, response)
        response.json.return_value["status"] = 401
        with self.assertRaisesRegex(AssertionError, "problem status"):
            hooks.after_call(None, case, response)
        response.headers = {"content-type": ["application/json"]}
        with self.assertRaisesRegex(AssertionError, "application/problem\\+json"):
            hooks.after_call(None, case, response)


if __name__ == "__main__":
    unittest.main()
