import EventKit
import Foundation

struct ConfirmedSystemReminder: Sendable {
    let id: String
    let title: String
    let dueAt: Date
    let timezone: String
    let recurrence: String
    let status: String
}

struct SystemReminderSyncResult: Sendable {
    let status: String
    let provider: String
    let externalID: String
    let revision: String
    let errorCode: String
}

@MainActor
final class SystemReminderSync {
    private let eventStore: EKEventStore

    init(eventStore: EKEventStore = EKEventStore()) {
        self.eventStore = eventStore
    }

    func synchronize(
        _ reminder: ConfirmedSystemReminder,
        existingExternalID: String? = nil
    ) async -> SystemReminderSyncResult {
        guard reminder.status == "active" else {
            return failure("reminder_not_confirmed")
        }
        guard let timezone = TimeZone(identifier: reminder.timezone) else {
            return failure("invalid_timezone")
        }

        do {
            guard try await hasFullAccess() else {
                return SystemReminderSyncResult(
                    status: "permission_denied",
                    provider: "ios_eventkit",
                    externalID: "",
                    revision: "",
                    errorCode: "permission_denied"
                )
            }
            guard let calendar = eventStore.defaultCalendarForNewReminders() else {
                return failure("default_calendar_unavailable")
            }

            let item = existingExternalID
                .flatMap { eventStore.calendarItem(withIdentifier: $0) as? EKReminder }
                ?? EKReminder(eventStore: eventStore)
            item.calendar = calendar
            item.title = reminder.title
            item.notes = "伴AI 提醒 · \(reminder.id)"
            item.dueDateComponents = dueComponents(for: reminder.dueAt, timezone: timezone)
            item.recurrenceRules = recurrenceRules(for: reminder.recurrence)
            try eventStore.save(item, commit: true)

            return SystemReminderSyncResult(
                status: "synced",
                provider: "ios_eventkit",
                externalID: item.calendarItemIdentifier,
                revision: revision(for: item),
                errorCode: ""
            )
        } catch {
            return failure(errorCode(for: error))
        }
    }

    private func hasFullAccess() async throws -> Bool {
        switch EKEventStore.authorizationStatus(for: .reminder) {
        case .fullAccess:
            return true
        case .notDetermined:
            return try await eventStore.requestFullAccessToReminders()
        case .denied, .restricted, .writeOnly:
            return false
        @unknown default:
            return false
        }
    }

    private func dueComponents(for date: Date, timezone: TimeZone) -> DateComponents {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = timezone
        var components = calendar.dateComponents([.year, .month, .day, .hour, .minute], from: date)
        components.calendar = calendar
        components.timeZone = timezone
        return components
    }

    private func recurrenceRules(for recurrence: String) -> [EKRecurrenceRule]? {
        let frequency: EKRecurrenceFrequency
        switch recurrence {
        case "daily": frequency = .daily
        case "weekly": frequency = .weekly
        default: return nil
        }
        return [EKRecurrenceRule(recurrenceWith: frequency, interval: 1, end: nil)]
    }

    private func revision(for reminder: EKReminder) -> String {
        String(Int((reminder.lastModifiedDate ?? Date()).timeIntervalSince1970 * 1_000))
    }

    private func errorCode(for error: Error) -> String {
        let nsError = error as NSError
        return "eventkit_\(nsError.domain)_\(nsError.code)"
            .lowercased()
            .replacingOccurrences(of: " ", with: "_")
    }

    private func failure(_ errorCode: String) -> SystemReminderSyncResult {
        SystemReminderSyncResult(
            status: "failed",
            provider: "ios_eventkit",
            externalID: "",
            revision: "",
            errorCode: errorCode
        )
    }
}
