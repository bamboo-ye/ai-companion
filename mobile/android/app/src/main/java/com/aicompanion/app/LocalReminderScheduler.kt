package com.aicompanion.app

import android.Manifest
import android.app.AlarmManager
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat

class LocalReminderScheduler(private val context: Context) {
    fun schedule(reminderId: String, title: String, dueAtEpochMillis: Long, exactRequested: Boolean): String {
        val alarmManager = context.getSystemService(AlarmManager::class.java)
        val operation = reminderIntent(context, reminderId, title)
        val canUseExact = exactRequested && (Build.VERSION.SDK_INT < Build.VERSION_CODES.S || alarmManager.canScheduleExactAlarms())
        if (canUseExact) {
            alarmManager.setExactAndAllowWhileIdle(AlarmManager.RTC_WAKEUP, dueAtEpochMillis, operation)
            return "scheduled_exact"
        }
        alarmManager.setAndAllowWhileIdle(AlarmManager.RTC_WAKEUP, dueAtEpochMillis, operation)
        return if (exactRequested) "scheduled_inexact_permission_unavailable" else "scheduled_inexact"
    }

    fun cancel(reminderId: String) {
        context.getSystemService(AlarmManager::class.java).cancel(reminderIntent(context, reminderId, ""))
    }

    private fun reminderIntent(context: Context, reminderId: String, title: String): PendingIntent {
        val intent = Intent(context, ReminderAlarmReceiver::class.java)
            .putExtra(ReminderAlarmReceiver.EXTRA_TITLE, title)
        return PendingIntent.getBroadcast(
            context,
            reminderId.hashCode(),
            intent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
    }
}

class ReminderAlarmReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            ContextCompat.checkSelfPermission(context, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) return

        val manager = context.getSystemService(NotificationManager::class.java)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            manager.createNotificationChannel(
                NotificationChannel(CHANNEL_ID, "伴AI 提醒", NotificationManager.IMPORTANCE_HIGH),
            )
        }
        val notification = NotificationCompat.Builder(context, CHANNEL_ID)
            .setSmallIcon(android.R.drawable.ic_dialog_info)
            .setContentTitle(intent.getStringExtra(EXTRA_TITLE) ?: "伴AI 提醒")
            .setContentText("该做这件事啦")
            .setPriority(NotificationCompat.PRIORITY_HIGH)
            .setAutoCancel(true)
            .build()
        NotificationManagerCompat.from(context).notify(System.currentTimeMillis().toInt(), notification)
    }

    companion object {
        const val EXTRA_TITLE = "reminder_title"
        private const val CHANNEL_ID = "ai_companion_reminders"
    }
}
