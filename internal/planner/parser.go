package planner

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	clockPattern        = regexp.MustCompile(`([0-9]{1,2}|[一二三四五六七八九十两]{1,3})点(?:(半)|([0-9]{1,2}|[一二三四五六七八九十两]{1,3})分?)?`)
	reminderDatePattern = regexp.MustCompile(`(20\d{2})[-年/](\d{1,2})[-月/](\d{1,2})日?`)
)

func LooksLikeReminder(text string) bool {
	return strings.Contains(text, "提醒") || strings.Contains(strings.ToLower(text), "remind")
}

func ParseReminder(text, timezone string, reference time.Time) (Reminder, error) {
	text = strings.TrimSpace(text)
	if len([]rune(text)) < 2 || len([]rune(text)) > 2000 {
		return Reminder{}, fmt.Errorf("%w: text must contain 2-2000 characters", ErrValidation)
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return Reminder{}, fmt.Errorf("%w: invalid timezone", ErrValidation)
	}
	result := Reminder{RawText: text, Timezone: timezone, Recurrence: detectRecurrence(text), NeedsClarification: []string{}, Status: "pending_confirmation", SystemSyncStatus: "not_requested"}
	result.Title = reminderTitle(text)
	if result.Title == "" {
		result.NeedsClarification = append(result.NeedsClarification, "title")
	}
	localReference := reference.In(location)
	due, precision, ok, invalidWallTime := parseReminderDue(text, localReference, location)
	if invalidWallTime {
		result.NeedsClarification = append(result.NeedsClarification, "dst_conflict")
	} else if ok {
		utc := due.UTC()
		result.DueAt = &utc
		result.LocalDue = due.Format("2006-01-02 15:04")
		result.TimePrecision = precision
	} else {
		result.NeedsClarification = append(result.NeedsClarification, "due_at")
	}
	if len(result.NeedsClarification) > 0 {
		result.Status = "needs_clarification"
	}
	return result, nil
}

func parseReminderDue(text string, reference time.Time, location *time.Location) (time.Time, string, bool, bool) {
	year, month, day := reference.Date()
	dateKnown := false
	if match := reminderDatePattern.FindStringSubmatch(text); len(match) == 4 {
		year, _ = strconv.Atoi(match[1])
		monthValue, _ := strconv.Atoi(match[2])
		day, _ = strconv.Atoi(match[3])
		month = time.Month(monthValue)
		dateKnown = true
	} else if strings.Contains(text, "明天") || strings.Contains(text, "明早") || strings.Contains(text, "明晚") {
		next := reference.AddDate(0, 0, 1)
		year, month, day = next.Date()
		dateKnown = true
	} else if strings.Contains(text, "今天") || strings.Contains(text, "今晚") {
		dateKnown = true
	}
	clock := clockPattern.FindStringSubmatch(text)
	if !dateKnown || len(clock) == 0 {
		return time.Time{}, "", false, false
	}
	hour, ok := chineseNumber(clock[1])
	if !ok || hour > 23 {
		return time.Time{}, "", false, false
	}
	minute := 0
	if clock[2] == "半" {
		minute = 30
	} else if clock[3] != "" {
		minute, ok = chineseNumber(clock[3])
		if !ok || minute > 59 {
			return time.Time{}, "", false, false
		}
	}
	if (strings.Contains(text, "晚") || strings.Contains(text, "下午")) && hour < 12 {
		hour += 12
	}
	if strings.Contains(text, "早") && hour == 12 {
		hour = 0
	}
	due := time.Date(year, month, day, hour, minute, 0, 0, location)
	local := due.In(location)
	if local.Year() != year || local.Month() != month || local.Day() != day || local.Hour() != hour || local.Minute() != minute {
		return time.Time{}, "", false, true
	}
	return due, "minute", true, false
}

func chineseNumber(value string) (int, bool) {
	if number, err := strconv.Atoi(value); err == nil {
		return number, true
	}
	digits := map[rune]int{'零': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	runes := []rune(value)
	if len(runes) == 1 {
		if runes[0] == '十' {
			return 10, true
		}
		result, ok := digits[runes[0]]
		return result, ok
	}
	if len(runes) == 2 && runes[0] == '十' {
		second, ok := digits[runes[1]]
		return 10 + second, ok
	}
	if len(runes) == 2 && runes[1] == '十' {
		first, ok := digits[runes[0]]
		return first * 10, ok
	}
	if len(runes) == 3 && runes[1] == '十' {
		first, ok1 := digits[runes[0]]
		last, ok2 := digits[runes[2]]
		return first*10 + last, ok1 && ok2
	}
	return 0, false
}

func detectRecurrence(text string) string {
	switch {
	case strings.Contains(text, "每天"):
		return "daily"
	case strings.Contains(text, "每周") || strings.Contains(text, "每星期"):
		return "weekly"
	default:
		return "none"
	}
}

func reminderTitle(text string) string {
	value := reminderDatePattern.ReplaceAllString(text, "")
	value = clockPattern.ReplaceAllString(value, "")
	for _, token := range []string{"请", "明天", "明早", "明晚", "今天", "今晚", "每天", "每周", "提醒我", "提醒", "一下"} {
		value = strings.ReplaceAll(value, token, "")
	}
	return strings.Trim(strings.TrimSpace(value), "，,。.!！?？ ")
}
