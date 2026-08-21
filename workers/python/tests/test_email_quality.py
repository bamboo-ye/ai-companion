from __future__ import annotations

import unittest

from ai_companion_worker.email_quality import (
    EMAIL_DRAFT_POLICY_VERSION,
    quality_report,
    requested_email_language,
    validate_email_arguments,
)


def complete_english_email() -> dict[str, object]:
    return {
        "to": [],
        "subject": "Question About Semester A Project Courses",
        "purpose": "Ask whether a Project course may be taken in Semester A.",
        "output_language": "en-US",
        "relationship": "first_contact",
        "introduction_policy": "required",
        "sender_name": "Alex Chen",
        "sender_role": "Prospective student",
        "salutation": "Dear Sir or Madam,",
        "introduction": "My name is Alex Chen, and I am a prospective student.",
        "body_paragraphs": [
            "I am planning my Semester A schedule and am considering the Project course."
        ],
        "request_or_next_step": "Could you please let me know whether any prerequisites apply?",
        "courtesy": "Thank you for your time and assistance.",
        "closing": "Kind regards,",
        "signature_lines": ["Alex Chen", "Prospective student"],
        "tone": "formal",
    }


class EmailQualityTest(unittest.TestCase):
    def test_explicit_english_request_has_highest_priority(self) -> None:
        self.assertEqual(
            requested_email_language("请帮我写一封英文邮件，咨询课程安排"),
            "en-US",
        )
        arguments = complete_english_email()
        self.assertEqual(
            validate_email_arguments(
                arguments,
                message="请帮我写一封英文邮件，咨询课程安排",
                email_profile={
                    "sender_name": "Alex Chen",
                    "default_language": "zh-CN",
                },
            ),
            [],
        )
        self.assertEqual(
            quality_report([]),
            {
                "passed": True,
                "policy_version": EMAIL_DRAFT_POLICY_VERSION,
                "violations": [],
            },
        )

    def test_mixed_language_missing_introduction_and_signature_are_rejected(self) -> None:
        arguments = complete_english_email()
        arguments["salutation"] = "您好！"
        arguments["introduction"] = ""
        arguments["body_paragraphs"] = ["请问可以选 Project 课吗？"]
        arguments["signature_lines"] = []
        violations = validate_email_arguments(
            arguments,
            message="写一封英文邮件",
            email_profile={"sender_name": "Alex Chen"},
        )
        for expected in (
            "missing_introduction",
            "missing_signature",
            "english_language_contamination",
            "english_salutation_not_polite",
        ):
            self.assertIn(expected, violations)

    def test_sender_profile_cannot_be_replaced_by_a_fabricated_identity(self) -> None:
        arguments = complete_english_email()
        arguments["sender_name"] = "Invented Person"
        arguments["introduction"] = "My name is Invented Person."
        arguments["signature_lines"] = ["Invented Person"]
        violations = validate_email_arguments(
            arguments,
            message="Write an English email",
            email_profile={"sender_name": "Alex Chen"},
        )
        self.assertIn("sender_identity_mismatch", violations)

    def test_repeated_request_or_courtesy_in_body_is_rejected(self) -> None:
        arguments = complete_english_email()
        arguments["body_paragraphs"] = [
            "Could you please let me know whether any prerequisites apply?",
            "Thank you for your time and assistance.",
        ]
        violations = validate_email_arguments(
            arguments,
            message="Write an English email",
            email_profile={"sender_name": "Alex Chen"},
        )
        self.assertIn("request_content_in_body", violations)
        self.assertIn("courtesy_content_in_body", violations)
        self.assertIn("duplicate_email_content", violations)

    def test_semantically_repeated_paragraphs_are_rejected(self) -> None:
        arguments = complete_english_email()
        arguments["body_paragraphs"] = [
            "The Project course is part of my planned Semester A schedule.",
            "My planned Semester A schedule includes the Project course.",
        ]
        violations = validate_email_arguments(
            arguments,
            message="Write an English email",
            email_profile={"sender_name": "Alex Chen"},
        )
        self.assertIn("duplicate_email_content", violations)


if __name__ == "__main__":
    unittest.main()
