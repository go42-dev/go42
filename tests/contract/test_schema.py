"""Validate request boundaries with the same schema conversion used by Schemathesis."""

from pathlib import Path
import unittest

from jsonschema_rs import Draft4Validator
from schemathesis.specs.openapi.converter import to_json_schema
import yaml


SCHEMA = yaml.safe_load((Path(__file__).resolve().parents[2] / "api/openapi/v1/auth.yaml").read_text())
PASSWORD = "Correct!password123"


def validator(name):
    return Draft4Validator(to_json_schema(SCHEMA["components"]["schemas"][name], nullable_keyword="nullable"))


class SchemaTests(unittest.TestCase):
    def test_known_errors_have_explicit_responses_instead_of_only_default(self):
        protected = []
        for path, methods in SCHEMA["paths"].items():
            for method, operation in methods.items():
                if operation.get("x-required-permissions"):
                    protected.append((method, path))
                    self.assertIn("403", operation["responses"], (method, path))
        self.assertEqual(7, len(protected))
        for path, method in (("/auth/signup", "post"), ("/users", "post"), ("/users/{uuid}", "put"), ("/users/me", "put")):
            self.assertIn("409", SCHEMA["paths"][path][method]["responses"])
        self.assertIn("400", SCHEMA["paths"]["/auth/login"]["post"]["responses"])
        self.assertNotIn("403", SCHEMA["paths"]["/auth/login"]["post"]["responses"])

    def test_self_update_requires_proof_for_each_nonnull_credential(self):
        check = validator("UpdateSelfRequest")
        for fields in (
            {"email": "user@example.com"}, {"password": PASSWORD},
            {"email": "user@example.com", "password": PASSWORD},
            {"email": "user@example.com", "password": None}, {"email": None, "password": PASSWORD},
        ):
            with self.subTest(fields=fields):
                self.assertTrue(check.is_valid(dict(fields, current_password=PASSWORD)))
                self.assertFalse(check.is_valid(fields))
                for proof in (None, "", "short", "x" * 73, 42):
                    self.assertFalse(check.is_valid(dict(fields, current_password=proof)))

    def test_self_update_noops_do_not_require_or_validate_password_proof(self):
        check = validator("UpdateSelfRequest")
        for fields in ({}, {"email": None}, {"password": None}, {"email": None, "password": None}):
            with self.subTest(fields=fields):
                self.assertTrue(check.is_valid(fields))
                for proof in (None, "", "wrong", "x" * 73):
                    self.assertTrue(check.is_valid(dict(fields, current_password=proof)))
                self.assertFalse(check.is_valid(dict(fields, current_password=42)))

    def test_token_schemas_distinguish_empty_from_nonempty_invalid_tokens(self):
        for name, field in (("RefreshRequest", "token"), ("LogoutRequest", "refresh_token")):
            check = validator(name)
            with self.subTest(schema=name):
                for body in ({}, {field: None}, {field: ""}, {field: 42}):
                    self.assertFalse(check.is_valid(body))
                for token in ("x", " \t\n", "not-a-token"):
                    self.assertTrue(check.is_valid({field: token}))
        self.assertTrue(validator("LogoutRequest").is_valid({"refresh_token": "x", "access_token": ""}))


if __name__ == "__main__":
    unittest.main()
