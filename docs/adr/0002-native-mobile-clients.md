# ADR 0002: Use native Android and iOS clients

- Status: Accepted
- Date: 2026-07-01

## Decision

Android uses Kotlin and Jetpack Compose. iOS uses Swift and SwiftUI. The clients share OpenAPI/event contracts, design tokens, and acceptance scenarios, but not UI source code.

System reminders are platform adapters: iOS uses EventKit Reminders; Android uses Calendar Provider and AlarmManager, with Google Tasks as an optional OAuth connector.

