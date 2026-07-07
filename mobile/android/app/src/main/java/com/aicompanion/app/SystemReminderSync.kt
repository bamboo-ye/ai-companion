package com.aicompanion.app

import android.Manifest
import android.content.ContentUris
import android.content.ContentValues
import android.content.Context
import android.content.pm.PackageManager
import android.provider.CalendarContract
import androidx.core.content.ContextCompat
import java.util.TimeZone

data class ConfirmedSystemReminder(
    val id: String,
    val title: String,
    val dueAtEpochMillis: Long,
    val timezone: String,
    val recurrence: String,
    val status: String,
)

data class SystemReminderSyncResult(
    val status: String,
    val provider: String = "android_calendar",
    val externalId: String = "",
    val revision: String = "",
    val errorCode: String = "",
)

class SystemReminderSync(private val context: Context) {
    fun synchronize(
        reminder: ConfirmedSystemReminder,
        existingExternalId: String? = null,
    ): SystemReminderSyncResult {
        if (reminder.status != "active") return failed("reminder_not_confirmed")
        if (TimeZone.getAvailableIDs().none { it == reminder.timezone }) return failed("invalid_timezone")
        if (!hasCalendarPermission()) {
            return SystemReminderSyncResult(status = "permission_denied", errorCode = "permission_denied")
        }

        return runCatching {
            val calendarId = writableCalendarId() ?: return failed("writable_calendar_unavailable")
            val values = ContentValues().apply {
                put(CalendarContract.Events.CALENDAR_ID, calendarId)
                put(CalendarContract.Events.TITLE, reminder.title)
                put(CalendarContract.Events.DESCRIPTION, "伴AI 提醒 · ${reminder.id}")
                put(CalendarContract.Events.DTSTART, reminder.dueAtEpochMillis)
                put(CalendarContract.Events.DTEND, reminder.dueAtEpochMillis + DEFAULT_DURATION_MILLIS)
                put(CalendarContract.Events.EVENT_TIMEZONE, reminder.timezone)
                put(CalendarContract.Events.HAS_ALARM, 1)
                put(CalendarContract.Events.RRULE, recurrenceRule(reminder.recurrence))
            }
            val eventId = existingExternalId?.toLongOrNull()?.let { existingId ->
                val uri = ContentUris.withAppendedId(CalendarContract.Events.CONTENT_URI, existingId)
                if (context.contentResolver.update(uri, values, null, null) > 0) existingId else null
            } ?: run {
                val uri = context.contentResolver.insert(CalendarContract.Events.CONTENT_URI, values)
                    ?: error("calendar_insert_failed")
                ContentUris.parseId(uri)
            }

            upsertAlert(eventId)
            SystemReminderSyncResult(
                status = "synced",
                externalId = eventId.toString(),
                revision = System.currentTimeMillis().toString(),
            )
        }.getOrElse { failed(it.message ?: "calendar_write_failed") }
    }

    private fun hasCalendarPermission(): Boolean =
        ContextCompat.checkSelfPermission(context, Manifest.permission.READ_CALENDAR) == PackageManager.PERMISSION_GRANTED &&
            ContextCompat.checkSelfPermission(context, Manifest.permission.WRITE_CALENDAR) == PackageManager.PERMISSION_GRANTED

    private fun writableCalendarId(): Long? {
        val projection = arrayOf(CalendarContract.Calendars._ID)
        val selection = "${CalendarContract.Calendars.VISIBLE}=1 AND ${CalendarContract.Calendars.CALENDAR_ACCESS_LEVEL}>=?"
        val args = arrayOf(CalendarContract.Calendars.CAL_ACCESS_CONTRIBUTOR.toString())
        context.contentResolver.query(
            CalendarContract.Calendars.CONTENT_URI,
            projection,
            selection,
            args,
            "${CalendarContract.Calendars.IS_PRIMARY} DESC, ${CalendarContract.Calendars._ID} ASC",
        )?.use { cursor ->
            if (cursor.moveToFirst()) return cursor.getLong(0)
        }
        return null
    }

    private fun upsertAlert(eventId: Long) {
        context.contentResolver.delete(
            CalendarContract.Reminders.CONTENT_URI,
            "${CalendarContract.Reminders.EVENT_ID}=?",
            arrayOf(eventId.toString()),
        )
        val reminderValues = ContentValues().apply {
            put(CalendarContract.Reminders.EVENT_ID, eventId)
            put(CalendarContract.Reminders.MINUTES, 0)
            put(CalendarContract.Reminders.METHOD, CalendarContract.Reminders.METHOD_ALERT)
        }
        checkNotNull(context.contentResolver.insert(CalendarContract.Reminders.CONTENT_URI, reminderValues)) {
            "calendar_alert_insert_failed"
        }
    }

    private fun recurrenceRule(recurrence: String): String? = when (recurrence) {
        "daily" -> "FREQ=DAILY"
        "weekly" -> "FREQ=WEEKLY"
        else -> null
    }

    private fun failed(code: String) = SystemReminderSyncResult(
        status = "failed",
        errorCode = code.lowercase().replace(' ', '_').take(128),
    )

    private companion object {
        const val DEFAULT_DURATION_MILLIS = 30 * 60 * 1_000L
    }
}
