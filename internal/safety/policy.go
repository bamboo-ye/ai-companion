package safety

import (
	"errors"
	"strings"
)

var ErrBlocked = errors.New("content blocked by safety policy")

type Policy struct{}

func (Policy) CheckInput(text string) error  { return check(text) }
func (Policy) CheckOutput(text string) error { return check(text) }
func check(text string) error {
	normalized := strings.ToLower(strings.ReplaceAll(text, " ", ""))
	blocked := []string{"儿童色情", "未成年人色情", "childsexualabusematerial", "csam制作", "窃取访问令牌", "stealaccesstoken"}
	for _, pattern := range blocked {
		if strings.Contains(normalized, pattern) {
			return ErrBlocked
		}
	}
	return nil
}
