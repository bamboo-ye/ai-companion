from __future__ import annotations

import unittest

from ai_companion_worker.tool_repair import (
    apply_known_repair,
    apply_restricted_patches,
    preflight_repair,
    repair_operator_allowed,
    safe_basename,
)


def filename_definition(extension: str = ".pptx") -> dict[str, object]:
    return {
        "repair_policies": [
            {
                "operator_id": "remove_optional_filename",
                "field_path": "/filename",
                "extension": extension,
                "semantics_preserving": True,
                "preflight": True,
            },
            {
                "operator_id": "filename.safe_basename",
                "field_path": "/filename",
                "extension": extension,
                "semantics_preserving": True,
                "preflight": True,
            },
        ]
    }


class ToolRepairTest(unittest.TestCase):
    def test_safe_basename_normalizes_separators_without_changing_title(self) -> None:
        title = "Important Dates - Semester A 2026/27"
        self.assertEqual(
            safe_basename(title, ".pptx"),
            "Important-Dates-Semester-A-2026-27.pptx",
        )
        self.assertEqual(title, "Important Dates - Semester A 2026/27")

    def test_safe_basename_handles_compatibility_separator_and_reserved_name(self) -> None:
        self.assertEqual(safe_basename("Ａ／Ｂ", ".pptx"), "A-B.pptx")
        self.assertEqual(safe_basename("CON", ".md"), "output-con.md")

    def test_preflight_repairs_explicit_model_filename(self) -> None:
        arguments = {
            "title": "Important Dates - Semester A 2026/27",
            "filename": "Important Dates - Semester A 2026/27.pptx",
        }
        repaired, records = preflight_repair(arguments, filename_definition())
        self.assertEqual(
            repaired["filename"], "Important-Dates-Semester-A-2026-27.pptx"
        )
        self.assertEqual(arguments["title"], "Important Dates - Semester A 2026/27")
        self.assertEqual(records[0]["operator_id"], "filename.safe_basename")

    def test_known_failure_removes_optional_filename_before_retry(self) -> None:
        repaired = apply_known_repair(
            {"title": "A/B", "filename": "A-B.pptx"},
            {
                "field_paths": ["/filename"],
                "allowed_repairs": [
                    "remove_optional_filename",
                    "filename.safe_basename",
                ],
            },
            filename_definition(),
        )
        self.assertIsNotNone(repaired)
        assert repaired is not None
        self.assertNotIn("filename", repaired["arguments"])

    def test_restricted_patch_rejects_unapproved_business_field(self) -> None:
        with self.assertRaisesRegex(ValueError, "path is not allowed"):
            apply_restricted_patches(
                {"filename": "safe.pptx", "title": "Original"},
                [{"op": "replace", "path": "/title", "value": "Changed"}],
                allowed_paths={"/filename"},
            )

    def test_operator_must_allow_every_changed_path(self) -> None:
        self.assertFalse(
            repair_operator_allowed(
                filename_definition(),
                "filename.safe_basename",
                ["/filename", "/title"],
            )
        )


if __name__ == "__main__":
    unittest.main()
