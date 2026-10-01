// 教学模拟，不是生产 Store：只使用标准库和内存，不连接数据库或外部服务。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

func must(ok bool, description string) {
	if !ok {
		panic("断言失败：" + description)
	}
}

// Run 只保留演示领取与迟到提交所需的字段。
// 时间用手动给出的整数表示，确保实验不依赖真实等待和调度顺序。
type Run struct {
	Status   string
	Owner    string
	Revision int
	LeaseEnd int
	Deadline int
}

type Store struct {
	mu  sync.Mutex
	run Run
}

func (s *Store) Claim(owner string, now, lease int) (Run, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	eligible := s.run.Status == "accepted" ||
		(s.run.Status == "running" && s.run.LeaseEnd <= now)
	if !eligible || s.run.Deadline <= now {
		return Run{}, false
	}
	s.run.Status = "running"
	s.run.Owner = owner
	s.run.Revision++
	s.run.LeaseEnd = now + lease
	return s.run, true // 按值返回，调用者保留当时快照。
}

func (s *Store) Complete(claim Run, now int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 判断和写入必须处于同一受保护区间；生产实现使用 SQL 条件更新。
	if s.run.Status != "running" || s.run.Owner != claim.Owner ||
		s.run.Revision != claim.Revision || s.run.LeaseEnd <= now || s.run.Deadline <= now {
		return false
	}
	s.run.Status = "completed"
	return true
}

func main() {
	text := "你好A"
	must(len(text) == 7 && len([]rune(text)) == 3, "UTF-8 字节与码点")
	fmt.Printf("字符实验：bytes=%d runes=%d\n", len(text), len([]rune(text)))

	for _, sample := range []struct {
		body string
		want string
	}{
		{`{}`, "missing"},
		{`{"approved":null}`, "missing"},
		{`{"approved":false}`, "rejected"},
		{`{"approved":true}`, "approved"},
	} {
		var input struct {
			Approved *bool `json:"approved"`
		}
		err := json.Unmarshal([]byte(sample.body), &input)
		must(err == nil, "JSON 应合法")
		state := "missing"
		if input.Approved != nil {
			state = "rejected"
			if *input.Approved {
				state = "approved"
			}
		}
		must(state == sample.want, "指针保留审批三态")
		fmt.Printf("审批实验：%s => %s\n", sample.body, state)
	}
	var malformed struct {
		Approved *bool `json:"approved"`
	}
	must(json.Unmarshal([]byte(`{"approved":"false"}`), &malformed) != nil, "字符串不是布尔值")

	store := &Store{run: Run{Status: "accepted", Revision: 1, Deadline: 100}}
	claimA, ok := store.Claim("A", 0, 10)
	must(ok && claimA.Revision == 2, "A 首次领取")
	_, ok = store.Claim("B", 5, 10)
	must(!ok, "A 租约有效时 B 不能接管")
	must(!store.Complete(claimA, 10), "过期瞬间即使未接管也不能完成")
	claimB, ok := store.Claim("B", 11, 10)
	must(ok && claimB.Revision == 3, "B 接管过期任务")
	lateA := store.Complete(claimA, 12)
	finishedB := store.Complete(claimB, 12)
	must(!lateA && finishedB, "旧快照被拒绝，新 owner 能完成")
	must(!store.Complete(claimB, 13), "不能重复完成终态任务")
	fmt.Printf("接管实验：A_rev=%d B_rev=%d late_A=%t current_B=%t\n", claimA.Revision, claimB.Revision, lateA, finishedB)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		result <- ctx.Err() // 主函数此时尚未接收；容量 1 允许这次发送完成。
		close(done)
	}()
	cancel()
	<-done // 确认发送者已经退出，然后才取结果。
	err := <-result
	must(err == context.Canceled, "取消信号传递")
	fmt.Printf("取消实验：%v，发送者已退出\n", err)
	fmt.Println("Go 实验全部通过（内存教学模型，不是数据库并发测试）")
}
