package ledger

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	amountPattern       = regexp.MustCompile(`(?i)(\d+(?:\.\d{1,2})?)\s*(人民币|美元|元|块|cny|rmb|usd|dollars?|¥|\$)`)
	explicitDatePattern = regexp.MustCompile(`(20\d{2})[-年/](\d{1,2})[-月/](\d{1,2})日?`)
)

func LooksLikeCandidate(text string) bool {
	if !amountPattern.MatchString(strings.TrimSpace(text)) {
		return false
	}
	return detectDirection(text) != "" || detectCategory(text) != "other"
}

func Parse(text, timezone string, reference time.Time) (Candidate, error) {
	text = strings.TrimSpace(text)
	if len([]rune(text)) < 2 || len([]rune(text)) > 2000 {
		return Candidate{}, fmt.Errorf("%w: text must contain 2-2000 characters", ErrValidation)
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return Candidate{}, fmt.Errorf("%w: invalid timezone", ErrValidation)
	}
	result := Candidate{RawText: text, Timezone: timezone, Direction: detectDirection(text), Category: detectCategory(text), Merchant: detectMerchant(text), NeedsClarification: []string{}, Status: "pending", Confidence: .25}
	match := amountPattern.FindStringSubmatch(text)
	if len(match) == 3 {
		amount, parseErr := strconv.ParseFloat(match[1], 64)
		if parseErr != nil || amount <= 0 || amount > 1_000_000_000 {
			return Candidate{}, fmt.Errorf("%w: invalid amount", ErrValidation)
		}
		result.AmountMinor = int64(math.Round(amount * 100))
		result.Currency = normalizeCurrency(match[2])
		result.Confidence += .3
	} else {
		result.NeedsClarification = append(result.NeedsClarification, "amount")
	}
	if result.Direction != "" {
		result.Confidence += .15
	} else {
		result.NeedsClarification = append(result.NeedsClarification, "direction")
	}
	if result.Category != "other" {
		result.Confidence += .1
	}
	occurredAt, precision, ok := parseOccurredAt(text, reference.In(location), location)
	if ok {
		utc := occurredAt.UTC()
		result.OccurredAt = &utc
		result.TimePrecision = precision
		result.Confidence += .2
	} else {
		result.NeedsClarification = append(result.NeedsClarification, "occurred_at")
	}
	if result.Confidence > 1 {
		result.Confidence = 1
	}
	if len(result.NeedsClarification) > 0 {
		result.Status = "needs_clarification"
	}
	return result, nil
}

func detectDirection(text string) string {
	lower := strings.ToLower(text)
	for _, keyword := range []string{"收入", "工资", "奖金", "到账", "收款", "income", "salary"} {
		if strings.Contains(lower, keyword) {
			return "income"
		}
	}
	for _, keyword := range []string{"花", "买", "支付", "付款", "消费", "打车", "吃", "支出", "expense", "paid", "spent"} {
		if strings.Contains(lower, keyword) {
			return "expense"
		}
	}
	return ""
}

func detectCategory(text string) string {
	lower := strings.ToLower(text)
	categories := []struct {
		name     string
		keywords []string
	}{
		{"transport", []string{"打车", "出租车", "地铁", "公交", "火车", "机票", "taxi", "uber"}},
		{"dining", []string{"吃饭", "早餐", "午餐", "晚餐", "咖啡", "外卖", "餐厅", "food", "coffee"}},
		{"salary", []string{"工资", "奖金", "salary", "bonus"}},
		{"housing", []string{"房租", "物业", "水电", "rent"}},
		{"shopping", []string{"购物", "买衣", "超市", "shopping"}},
		{"health", []string{"医院", "药店", "体检", "health"}},
	}
	for _, category := range categories {
		for _, keyword := range category.keywords {
			if strings.Contains(lower, keyword) {
				return category.name
			}
		}
	}
	return "other"
}

func detectMerchant(text string) string {
	for _, merchant := range []string{"滴滴", "美团", "饿了么", "星巴克", "京东", "淘宝"} {
		if strings.Contains(text, merchant) {
			return merchant
		}
	}
	if strings.Contains(text, "打车") || strings.Contains(strings.ToLower(text), "taxi") {
		return "出租车"
	}
	return ""
}

func normalizeCurrency(value string) string {
	switch strings.ToLower(value) {
	case "美元", "usd", "dollar", "dollars", "$":
		return "USD"
	default:
		return "CNY"
	}
}

func parseOccurredAt(text string, reference time.Time, location *time.Location) (time.Time, string, bool) {
	if match := explicitDatePattern.FindStringSubmatch(text); len(match) == 4 {
		year, _ := strconv.Atoi(match[1])
		month, _ := strconv.Atoi(match[2])
		day, _ := strconv.Atoi(match[3])
		value := time.Date(year, time.Month(month), day, 12, 0, 0, 0, location)
		if value.Year() == year && int(value.Month()) == month && value.Day() == day {
			return value, "date", true
		}
		return time.Time{}, "", false
	}
	base := reference
	switch {
	case strings.Contains(text, "昨晚"):
		return time.Date(base.Year(), base.Month(), base.Day()-1, 20, 0, 0, 0, location), "part_of_day", true
	case strings.Contains(text, "昨天"):
		return time.Date(base.Year(), base.Month(), base.Day()-1, 12, 0, 0, 0, location), "date", true
	case strings.Contains(text, "今天") || strings.Contains(text, "刚刚"):
		return time.Date(base.Year(), base.Month(), base.Day(), base.Hour(), base.Minute(), 0, 0, location), "minute", true
	}
	return time.Time{}, "", false
}

func sortCategoryTotals(items []CategoryTotal) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].AmountMinor == items[j].AmountMinor {
			return items[i].Category < items[j].Category
		}
		return items[i].AmountMinor > items[j].AmountMinor
	})
}
