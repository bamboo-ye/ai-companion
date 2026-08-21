package skill

import (
	"context"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
)

const EmailDraftPolicyVersion = "email-draft-policy-2.1.0"

var (
	hanCharacter         = regexp.MustCompile(`[\p{Han}]`)
	emailRequestInBody   = regexp.MustCompile(`(?i)(\?|\b(could|would|can|will)\s+you\b|\bplease\s+(let|tell|confirm|advise|provide|share|reply|respond|send|clarify|explain)\b|\bi\s+am\s+writing\s+to\s+(ask|inquire|request)\b|\bi\s+would\s+(like|appreciate)\s+to\s+(know|ask|request)\b|请问|烦请|劳烦|能否|可否|是否可以|请(您)?(告知|确认|回复|提供|说明|协助)|希望(您)?)`)
	emailCourtesyInBody  = regexp.MustCompile(`(?i)(\bthank(s|\s+you)?\b|\bappreciat[a-z]*\b|\bgrateful\b|感谢|谢谢|感激)`)
	emailComparableToken = regexp.MustCompile(`[a-z0-9]+|[\p{Han}]`)
)

var emailDuplicateStopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "be": true,
	"for": true, "i": true, "in": true, "is": true, "it": true,
	"me": true, "my": true, "of": true, "on": true, "or": true,
	"please": true, "that": true, "the": true, "this": true, "to": true,
	"we": true, "you": true, "your": true,
}

func emailDraftHandler(_ context.Context, input map[string]any) (ToolResult, error) {
	recipients, err := emailRecipients(input["to"])
	if err != nil {
		return ToolResult{}, err
	}
	subject := emailText(input["subject"])
	purpose := emailText(input["purpose"])
	if subject == "" || purpose == "" {
		return ToolResult{}, fmt.Errorf("%w: subject and purpose are required", ErrValidation)
	}
	tone := strings.ToLower(emailText(input["tone"]))
	if tone == "" {
		tone = "formal"
	}
	language := emailText(input["output_language"])
	violations := validateStructuredEmailDraft(input)
	if len(violations) > 0 {
		return ToolResult{}, fmt.Errorf(
			"%w: email quality gate failed: %s",
			ErrValidation,
			strings.Join(violations, ","),
		)
	}
	body := renderStructuredEmailDraft(input)
	return ToolResult{Output: map[string]any{
		"to": recipients, "subject": subject, "body": body,
		"tone": tone, "output_language": language,
		"relationship": emailText(input["relationship"]),
		"send_status":  "draft_only", "provider_message_id": "",
		"email_policy_version": EmailDraftPolicyVersion,
		"quality_report": map[string]any{
			"passed": true, "policy_version": EmailDraftPolicyVersion,
			"violations": []string{},
		},
	}}, nil
}

func validateStructuredEmailDraft(input map[string]any) []string {
	violations := make([]string, 0)
	language := emailText(input["output_language"])
	if language != "en-US" && language != "zh-CN" {
		violations = append(violations, "unsupported_output_language")
	}
	relationship := emailText(input["relationship"])
	if !emailOneOf(relationship, "first_contact", "ongoing", "reply", "unknown") {
		violations = append(violations, "invalid_relationship")
	}
	introductionPolicy := emailText(input["introduction_policy"])
	if !emailOneOf(introductionPolicy, "required", "auto", "omit") {
		violations = append(violations, "invalid_introduction_policy")
	}
	for _, field := range []string{
		"sender_name", "subject", "purpose", "salutation",
		"request_or_next_step", "courtesy", "closing", "tone",
	} {
		if emailText(input[field]) == "" {
			violations = append(violations, "missing_"+field)
		}
	}
	paragraphs, ok := emailTextList(input["body_paragraphs"])
	if !ok || len(paragraphs) == 0 {
		violations = append(violations, "missing_body_paragraphs")
	} else if len(paragraphs) > 6 {
		violations = append(violations, "too_many_body_paragraphs")
	}
	for _, paragraph := range paragraphs {
		if emailRequestInBody.MatchString(paragraph) {
			violations = append(violations, "request_content_in_body")
		}
		if emailCourtesyInBody.MatchString(paragraph) {
			violations = append(violations, "courtesy_content_in_body")
		}
	}
	signatureLines, ok := emailTextList(input["signature_lines"])
	if !ok || len(signatureLines) == 0 {
		violations = append(violations, "missing_signature")
	} else if len(signatureLines) > 4 {
		violations = append(violations, "signature_too_long")
	}
	senderName := emailText(input["sender_name"])
	introduction := emailText(input["introduction"])
	introductionRequired := introductionPolicy == "required" ||
		(introductionPolicy != "omit" && emailOneOf(relationship, "first_contact", "unknown"))
	if relationship == "first_contact" && introductionPolicy == "omit" {
		violations = append(violations, "first_contact_introduction_omitted")
	} else if introductionRequired && introduction == "" {
		violations = append(violations, "missing_introduction")
	}
	if introduction != "" && senderName != "" && !strings.Contains(strings.ToLower(introduction), strings.ToLower(senderName)) {
		violations = append(violations, "introduction_missing_sender")
	}
	signatureHasSender := false
	for _, line := range signatureLines {
		if senderName != "" && strings.Contains(strings.ToLower(line), strings.ToLower(senderName)) {
			signatureHasSender = true
			break
		}
	}
	if len(signatureLines) > 0 && senderName != "" && !signatureHasSender {
		violations = append(violations, "signature_missing_sender")
	}
	contentSections := []string{introduction}
	contentSections = append(contentSections, paragraphs...)
	contentSections = append(contentSections, emailText(input["request_or_next_step"]), emailText(input["courtesy"]))
	if emailHasDuplicateContent(contentSections) {
		violations = append(violations, "duplicate_email_content")
	}
	if language == "en-US" {
		englishFields := []string{
			emailText(input["subject"]), emailText(input["salutation"]), introduction,
			emailText(input["request_or_next_step"]), emailText(input["courtesy"]), emailText(input["closing"]),
		}
		englishFields = append(englishFields, paragraphs...)
		for _, value := range englishFields {
			candidate := strings.ReplaceAll(value, senderName, "")
			if hanCharacter.MatchString(candidate) {
				violations = append(violations, "english_language_contamination")
				break
			}
		}
		salutation := strings.ToLower(emailText(input["salutation"]))
		if salutation != "" && !emailStartsWithAny(salutation, "dear ", "hello ", "hi ", "to ") {
			violations = append(violations, "english_salutation_not_polite")
		}
		courtesy := strings.ToLower(emailText(input["courtesy"]))
		if courtesy != "" && !emailContainsAny(courtesy, "thank", "appreciat", "grateful") {
			violations = append(violations, "english_courtesy_missing")
		}
		closing := strings.ToLower(emailText(input["closing"]))
		if closing != "" && !emailContainsAny(closing, "sincerely", "regards", "respectfully", "best wishes", "yours") {
			violations = append(violations, "english_closing_invalid")
		}
	}
	return emailUnique(violations)
}

func renderStructuredEmailDraft(input map[string]any) string {
	parts := make([]string, 0, 12)
	parts = emailAppendUniquePart(parts, emailText(input["salutation"]))
	if introduction := emailText(input["introduction"]); introduction != "" {
		parts = emailAppendUniquePart(parts, introduction)
	}
	paragraphs, _ := emailTextList(input["body_paragraphs"])
	for _, paragraph := range paragraphs {
		parts = emailAppendUniquePart(parts, paragraph)
	}
	parts = emailAppendUniquePart(parts, emailText(input["request_or_next_step"]))
	parts = emailAppendUniquePart(parts, emailText(input["courtesy"]))
	parts = emailAppendUniquePart(parts, emailText(input["closing"]))
	signatureLines, _ := emailTextList(input["signature_lines"])
	parts = emailAppendUniquePart(parts, strings.Join(signatureLines, "\n"))
	return strings.Join(parts, "\n\n")
}

func emailHasDuplicateContent(values []string) bool {
	for index, value := range values {
		if value == "" {
			continue
		}
		for _, candidate := range values[index+1:] {
			if candidate != "" && emailContentSimilar(value, candidate) {
				return true
			}
		}
	}
	return false
}

func emailContentSimilar(left, right string) bool {
	leftNormalized := emailComparableContent(left)
	rightNormalized := emailComparableContent(right)
	if leftNormalized == "" || rightNormalized == "" {
		return false
	}
	if leftNormalized == rightNormalized {
		return true
	}
	shorter, longer := leftNormalized, rightNormalized
	if len([]rune(shorter)) > len([]rune(longer)) {
		shorter, longer = longer, shorter
	}
	shorterLength := len([]rune(shorter))
	longerLength := len([]rune(longer))
	if shorterLength >= 24 && strings.Contains(longer, shorter) && float64(shorterLength)/float64(longerLength) >= 0.7 {
		return true
	}

	leftTokens := emailContentTokenSet(left)
	rightTokens := emailContentTokenSet(right)
	minimum := len(leftTokens)
	if len(rightTokens) < minimum {
		minimum = len(rightTokens)
	}
	if minimum < 4 {
		return false
	}
	overlap := 0
	for token := range leftTokens {
		if rightTokens[token] {
			overlap++
		}
	}
	if overlap < 4 || float64(overlap)/float64(minimum) < 0.8 {
		return false
	}
	union := len(leftTokens) + len(rightTokens) - overlap
	return union > 0 && float64(overlap)/float64(union) >= 0.55
}

func emailComparableContent(value string) string {
	return strings.Join(emailComparableToken.FindAllString(strings.ToLower(value), -1), "")
}

func emailContentTokenSet(value string) map[string]bool {
	result := make(map[string]bool)
	for _, token := range emailComparableToken.FindAllString(strings.ToLower(value), -1) {
		if !emailDuplicateStopWords[token] {
			result[token] = true
		}
	}
	return result
}

func emailAppendUniquePart(parts []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return parts
	}
	normalized := emailComparableContent(value)
	for _, existing := range parts {
		if normalized != "" && emailComparableContent(existing) == normalized {
			return parts
		}
	}
	return append(parts, value)
}

func emailRecipients(value any) ([]string, error) {
	rawRecipients := make([]any, 0)
	switch values := value.(type) {
	case nil:
	case []any:
		rawRecipients = values
	case []string:
		rawRecipients = make([]any, 0, len(values))
		for _, item := range values {
			rawRecipients = append(rawRecipients, item)
		}
	default:
		return nil, fmt.Errorf("%w: recipients must be an array", ErrValidation)
	}
	recipients := make([]string, 0, len(rawRecipients))
	for _, value := range rawRecipients {
		address := strings.TrimSpace(fmt.Sprint(value))
		parsed, err := mail.ParseAddress(address)
		if err != nil || parsed.Address != address {
			return nil, fmt.Errorf("%w: invalid recipient %q", ErrValidation, address)
		}
		recipients = append(recipients, address)
	}
	return recipients, nil
}

func emailText(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func emailTextList(value any) ([]string, bool) {
	result := make([]string, 0)
	switch values := value.(type) {
	case []string:
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				result = append(result, value)
			}
		}
		return result, true
	case []any:
		for _, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil, false
			}
			if text = strings.TrimSpace(text); text != "" {
				result = append(result, text)
			}
		}
		return result, true
	default:
		return nil, false
	}
}

func emailOneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func emailStartsWithAny(value string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func emailContainsAny(value string, tokens ...string) bool {
	for _, token := range tokens {
		if strings.Contains(value, token) {
			return true
		}
	}
	return false
}

func emailUnique(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}
