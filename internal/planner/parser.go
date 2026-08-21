package planner

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	clockPattern             = regexp.MustCompile(`([0-9]{1,2}|[一二三四五六七八九十两]{1,3})点(?:(半)|([0-9]{1,2}|[一二三四五六七八九十两]{1,3})分?)?`)
	colonClockPattern        = regexp.MustCompile(`([01]?[0-9]|2[0-3])[:：]([0-5][0-9])`)
	canonicalDateTimePattern = regexp.MustCompile(`^\s*20\d{2}-\d{2}-\d{2}\s+(?:[01]\d|2[0-3]):[0-5]\d\s*$`)
	reminderDatePattern      = regexp.MustCompile(`(20\d{2})[-年/](\d{1,2})[-月/](\d{1,2})日?`)
	reminderMonthDayPattern  = regexp.MustCompile(`(\d{1,2})\s*月\s*(\d{1,2})\s*(?:日|号)?`)
	reminderDayPattern       = regexp.MustCompile(`(\d{1,2})\s*(?:日|号)`)
	relativeWeekPattern      = regexp.MustCompile(`(每|这|本|下下|下个|下)?(?:周|星期|礼拜)(?:(一|二|三|四|五|六|日|天|末)(?:之前|以前|前)?|(?:内|结束|截止|最后)(?:之前|以前|前)?)`)
)

type Schedule struct {
	DueAt         time.Time `json:"due_at"`
	LocalDue      string    `json:"local_due"`
	TimePrecision string    `json:"time_precision"`
}

func LooksLikeReminder(text string) bool {
	return strings.Contains(text, "提醒") || strings.Contains(strings.ToLower(text), "remind")
}

func LooksLikeScheduleChange(text string) bool {
	for _, token := range []string{"改到", "改成", "改为", "调整到", "调整为", "安排到", "安排在", "设置到", "设置为", "设为", "挪到", "推迟到", "提前到"} {
		if strings.Contains(text, token) {
			return true
		}
	}
	return false
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
	due, precision, ok, issue := parseReminderDue(text, localReference, location)
	switch issue {
	case "invalid":
		result.NeedsClarification = append(result.NeedsClarification, "dst_conflict")
	case "day_period":
		result.NeedsClarification = append(result.NeedsClarification, "day_period")
	case "past_time":
		result.NeedsClarification = append(result.NeedsClarification, "future_time")
	case "":
		if !ok {
			result.NeedsClarification = append(result.NeedsClarification, "due_at")
			break
		}
		utc := due.UTC()
		result.DueAt = &utc
		if precision == "date" {
			result.LocalDue = due.Format("2006-01-02")
		} else {
			result.LocalDue = due.Format("2006-01-02 15:04")
		}
		result.TimePrecision = precision
	}
	if len(result.NeedsClarification) > 0 {
		result.Status = "needs_clarification"
	}
	return result, nil
}

// ParseReminderWithSlots merges independently resolved reminder fields with
// the current utterance. This supports both complete one-turn requests and
// follow-up answers that only fill a field requested in the previous turn.
func ParseReminderWithSlots(text, title, dateHint, timezone string, reference time.Time) (Reminder, error) {
	combined := strings.TrimSpace(text)
	for _, slot := range []string{strings.TrimSpace(dateHint), strings.TrimSpace(title)} {
		if slot != "" && !strings.Contains(combined, slot) {
			combined = strings.TrimSpace(combined + " " + slot)
		}
	}
	result, err := ParseReminder(combined, timezone, reference)
	if err != nil {
		return Reminder{}, err
	}
	if title = strings.TrimSpace(title); title != "" {
		if len([]rune(title)) > 255 {
			return Reminder{}, fmt.Errorf("%w: title must not exceed 255 characters", ErrValidation)
		}
		result.Title = title
		result.NeedsClarification = removeClarification(result.NeedsClarification, "title")
	}
	if len(result.NeedsClarification) == 0 {
		result.Status = "pending_confirmation"
	} else {
		result.Status = "needs_clarification"
	}
	return result, nil
}

// ParseSchedule extracts a date or date-time from natural language. If text
// only supplies a clock time, fallbackDate provides the date to preserve.
func ParseSchedule(text, timezone string, reference time.Time, fallbackDate *time.Time) (Schedule, error) {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return Schedule{}, fmt.Errorf("%w: invalid timezone", ErrValidation)
	}
	due, precision, ok, issue := parseReminderDue(text, reference.In(location), location)
	if !ok && issue == "" && fallbackDate != nil && hasClock(text) {
		withDate := fallbackDate.In(location).Format("2006-01-02") + " " + text
		due, precision, ok, issue = parseReminderDue(withDate, reference.In(location), location)
	}
	switch issue {
	case "invalid":
		return Schedule{}, fmt.Errorf("%w: invalid local date or time", ErrValidation)
	case "day_period":
		return Schedule{}, ErrDayPeriodRequired
	case "past_time":
		return Schedule{}, ErrPastSchedule
	}
	if !ok {
		return Schedule{}, fmt.Errorf("%w: schedule date or time is required", ErrValidation)
	}
	localDue := due.Format("2006-01-02 15:04")
	if precision == "date" {
		localDue = due.Format("2006-01-02")
	}
	return Schedule{DueAt: due.UTC(), LocalDue: localDue, TimePrecision: precision}, nil
}

func parseReminderDue(text string, reference time.Time, location *time.Location) (time.Time, string, bool, string) {
	year, month, day := reference.Date()
	dateKnown := false
	if match := reminderDatePattern.FindStringSubmatch(text); len(match) == 4 {
		year, _ = strconv.Atoi(match[1])
		monthValue, _ := strconv.Atoi(match[2])
		day, _ = strconv.Atoi(match[3])
		month = time.Month(monthValue)
		dateKnown = true
	} else if match := reminderMonthDayPattern.FindStringSubmatch(text); len(match) == 3 {
		monthValue, _ := strconv.Atoi(match[1])
		dayValue, _ := strconv.Atoi(match[2])
		candidate, ok := nextMonthDay(reference, time.Month(monthValue), dayValue, location)
		if !ok {
			return time.Time{}, "", false, "invalid"
		}
		year, month, day = candidate.Date()
		dateKnown = true
	} else if match := reminderDayPattern.FindStringSubmatch(text); len(match) == 2 {
		dayValue, _ := strconv.Atoi(match[1])
		candidate, ok := nextDayOfMonth(reference, dayValue, location)
		if !ok {
			return time.Time{}, "", false, "invalid"
		}
		year, month, day = candidate.Date()
		dateKnown = true
	} else if candidate, matched := relativeWeekDate(text, reference, location); matched {
		year, month, day = candidate.Date()
		dateKnown = true
	} else if strings.Contains(text, "明天") || strings.Contains(text, "明早") || strings.Contains(text, "明晚") {
		next := reference.AddDate(0, 0, 1)
		year, month, day = next.Date()
		dateKnown = true
	} else if strings.Contains(text, "今天") || strings.Contains(text, "今晚") {
		dateKnown = true
	}
	if !dateKnown {
		return time.Time{}, "", false, ""
	}
	hour, minute, clockKnown, clockValid := parseClock(text)
	if !clockKnown {
		due := time.Date(year, month, day, 0, 0, 0, 0, location)
		local := due.In(location)
		if local.Year() != year || local.Month() != month || local.Day() != day {
			return time.Time{}, "", false, "invalid"
		}
		if due.Before(localDateStart(reference, location)) {
			return time.Time{}, "", false, "past_time"
		}
		return due, "date", true, ""
	}
	if !clockValid {
		return time.Time{}, "", false, "invalid"
	}
	period := dayPeriod(text)
	if period == "afternoon" && hour < 12 {
		hour += 12
	}
	if period == "morning" && hour == 12 {
		hour = 0
	}
	if period != "" || canonicalDateTimePattern.MatchString(text) || hour == 0 || hour > 12 {
		due, valid := localMinute(year, month, day, hour, minute, location)
		if !valid {
			return time.Time{}, "", false, "invalid"
		}
		if !due.After(reference) {
			return time.Time{}, "", false, "past_time"
		}
		return due, "minute", true, ""
	}

	// Without an explicit day period, a 1-12 o'clock expression has two
	// possible wall-clock meanings. Infer it only when exactly one meaning is
	// still in the future; otherwise ask or reject instead of guessing.
	morningHour, afternoonHour := hour, hour+12
	if hour == 12 {
		morningHour, afternoonHour = 0, 12
	}
	morning, morningValid := localMinute(year, month, day, morningHour, minute, location)
	afternoon, afternoonValid := localMinute(year, month, day, afternoonHour, minute, location)
	if !morningValid || !afternoonValid {
		return time.Time{}, "", false, "invalid"
	}
	future := make([]time.Time, 0, 2)
	if morning.After(reference) {
		future = append(future, morning)
	}
	if afternoon.After(reference) {
		future = append(future, afternoon)
	}
	switch len(future) {
	case 1:
		return future[0], "minute", true, ""
	case 2:
		return time.Time{}, "", false, "day_period"
	default:
		return time.Time{}, "", false, "past_time"
	}
}

func relativeWeekDate(text string, reference time.Time, location *time.Location) (time.Time, bool) {
	match := relativeWeekPattern.FindStringSubmatchIndex(text)
	if len(match) == 0 {
		return time.Time{}, false
	}
	prefix := ""
	if match[2] >= 0 {
		prefix = text[match[2]:match[3]]
	}
	dayToken := ""
	if match[4] >= 0 {
		dayToken = text[match[4]:match[5]]
	}
	weekOffset := 0
	switch prefix {
	case "下", "下个":
		weekOffset = 1
	case "下下":
		weekOffset = 2
	}
	weekday := 6 // Week-ending expressions and 周末 resolve to Sunday.
	if dayToken != "" {
		weekday = map[string]int{"一": 0, "二": 1, "三": 2, "四": 3, "五": 4, "六": 5, "日": 6, "天": 6, "末": 6}[dayToken]
	}
	localToday := localDateStart(reference, location)
	daysSinceMonday := (int(localToday.Weekday()) + 6) % 7
	weekStart := localToday.AddDate(0, 0, -daysSinceMonday)
	candidate := weekStart.AddDate(0, 0, weekOffset*7+weekday)
	// An unqualified weekday means its nearest non-past occurrence. Explicit
	// current/next-week expressions retain their stated week instead.
	if (prefix == "" || prefix == "每") && candidate.Before(localToday) {
		candidate = candidate.AddDate(0, 0, 7)
	}
	return candidate, true
}

func dayPeriod(text string) string {
	clockIndex := clockPattern.FindStringIndex(text)
	if clockIndex == nil {
		clockIndex = colonClockPattern.FindStringIndex(text)
	}
	if clockIndex == nil {
		return ""
	}
	prefix := strings.TrimSpace(text[:clockIndex[0]])
	prefix = strings.TrimSpace(strings.TrimSuffix(prefix, "的"))
	for _, token := range []string{"下午", "晚上", "今晚", "明晚", "中午", "晚"} {
		if strings.HasSuffix(prefix, token) {
			return "afternoon"
		}
	}
	for _, token := range []string{"上午", "早上", "今早", "明早", "清晨", "早"} {
		if strings.HasSuffix(prefix, token) {
			return "morning"
		}
	}
	return ""
}

func localMinute(year int, month time.Month, day, hour, minute int, location *time.Location) (time.Time, bool) {
	due := time.Date(year, month, day, hour, minute, 0, 0, location)
	local := due.In(location)
	valid := local.Year() == year && local.Month() == month && local.Day() == day && local.Hour() == hour && local.Minute() == minute
	return due, valid
}

func hasClock(text string) bool {
	return clockPattern.MatchString(text) || colonClockPattern.MatchString(text)
}

func parseClock(text string) (int, int, bool, bool) {
	if clock := clockPattern.FindStringSubmatch(text); len(clock) > 0 {
		hour, ok := chineseNumber(clock[1])
		if !ok || hour > 23 {
			return 0, 0, true, false
		}
		minute := 0
		if clock[2] == "半" {
			minute = 30
		} else if clock[3] != "" {
			minute, ok = chineseNumber(clock[3])
			if !ok || minute > 59 {
				return 0, 0, true, false
			}
		}
		return hour, minute, true, true
	}
	if clock := colonClockPattern.FindStringSubmatch(text); len(clock) == 3 {
		hour, hourErr := strconv.Atoi(clock[1])
		minute, minuteErr := strconv.Atoi(clock[2])
		return hour, minute, true, hourErr == nil && minuteErr == nil
	}
	return 0, 0, false, true
}

func nextMonthDay(reference time.Time, month time.Month, day int, location *time.Location) (time.Time, bool) {
	if month < time.January || month > time.December || day < 1 || day > 31 {
		return time.Time{}, false
	}
	referenceDate := localDateStart(reference, location)
	for yearOffset := 0; yearOffset <= 8; yearOffset++ {
		candidate := time.Date(referenceDate.Year()+yearOffset, month, day, 0, 0, 0, 0, location)
		if candidate.Month() != month || candidate.Day() != day || candidate.Before(referenceDate) {
			continue
		}
		return candidate, true
	}
	return time.Time{}, false
}

func nextDayOfMonth(reference time.Time, day int, location *time.Location) (time.Time, bool) {
	if day < 1 || day > 31 {
		return time.Time{}, false
	}
	referenceDate := localDateStart(reference, location)
	monthStart := time.Date(referenceDate.Year(), referenceDate.Month(), 1, 0, 0, 0, 0, location)
	for monthOffset := 0; monthOffset <= 24; monthOffset++ {
		currentMonth := monthStart.AddDate(0, monthOffset, 0)
		candidate := time.Date(currentMonth.Year(), currentMonth.Month(), day, 0, 0, 0, 0, location)
		if candidate.Month() != currentMonth.Month() || candidate.Day() != day || candidate.Before(referenceDate) {
			continue
		}
		return candidate, true
	}
	return time.Time{}, false
}

func localDateStart(value time.Time, location *time.Location) time.Time {
	local := value.In(location)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
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
	value = reminderMonthDayPattern.ReplaceAllString(value, "")
	value = reminderDayPattern.ReplaceAllString(value, "")
	value = relativeWeekPattern.ReplaceAllString(value, "")
	value = clockPattern.ReplaceAllString(value, "")
	value = colonClockPattern.ReplaceAllString(value, " ")
	for _, token := range []string{"请", "明天", "明早", "明晚", "今天", "今晚", "每天", "每周", "提醒我", "提醒", "一下"} {
		value = strings.ReplaceAll(value, token, "")
	}
	return strings.Trim(strings.TrimSpace(value), "，,。.!！?？ ")
}

func removeClarification(values []string, target string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
}
