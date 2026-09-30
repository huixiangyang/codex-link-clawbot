package bridge

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/request"
)

// 一条实际微信流程覆盖：忙碌拒绝、数字菜单、并行会话、精确打断、重投不执行。
func TestBusyMessageOpensSessionMenuAndNeverRunsLater(t *testing.T) {
	f := newNumberMenuFixture(t)
	started := make(chan string, 4)
	f.agent.chat = func(ctx context.Context, id string, _ codex.ChatRequest) (string, error) {
		started <- id
		<-ctx.Done()
		return "", ctx.Err()
	}
	workspace := f.h.projects.List()[0]
	other, err := f.agent.StartThread(context.Background(), workspace.Root)
	if err != nil {
		t.Fatal(err)
	}
	f.input(t, "执行第一项工作")
	var firstThread string
	select {
	case firstThread = <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("first session did not start")
	}
	first := f.h.tasks.List("owner")[0]
	rejectedID := f.input(t, "不要偷偷执行这条追加指令")
	page := f.h.menus.session("owner").page
	if page.Title != "提交失败" || !strings.Contains(page.Notice, "未提交") || page.actions[2].taskID != first.ID || len(f.h.tasks.List("owner")) != 1 {
		t.Fatalf("busy response: %+v", page)
	}
	for _, n := range []int{1, 2, 3, 4} {
		if _, ok := page.actions[n]; !ok {
			t.Fatalf("missing mobile action %d", n)
		}
	}
	f.sequence++
	imageID := f.sequence
	if err := f.h.HandleMessage(context.Background(), f.client, ilink.WeixinMessage{MessageID: imageID, FromUserID: "owner", MessageType: ilink.MessageTypeUser, MessageState: ilink.MessageStateFinish, ContextToken: "fresh-context", ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeImage, ImageItem: &ilink.ImageItem{URL: "http://127.0.0.1:1/never-download.png"}}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.h.tasks.FindRejection(sourceForFixture(t, f, imageID)); !ok {
		t.Fatal("busy image was downloaded instead of immediately rejected")
	}
	f.input(t, "3")
	selection := 0
	for n, action := range f.h.menus.session("owner").page.actions {
		if action.threadID == other.ID {
			selection = n
		}
	}
	if selection == 0 {
		t.Fatal("other session not listed")
	}
	f.input(t, strconv.Itoa(selection))
	if f.h.targets.Current("owner").ThreadID != other.ID {
		t.Fatal("switch blocked by active session")
	}
	f.input(t, "执行第二项独立工作")
	select {
	case id := <-started:
		if id != other.ID {
			t.Fatal("wrong session executed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("independent session waited")
	}
	f.send(t, rejectedID, "不要偷偷执行这条追加指令")
	if page := f.h.menus.session("owner").page; page.actions[2].threadID != firstThread {
		t.Fatalf("replayed rejection lost original target: %+v", page)
	}
	f.input(t, "2")
	if action := f.h.menus.session("owner").page.actions[1]; action.taskID != first.ID {
		t.Fatal("confirmation drifted to new session")
	}
	confirmID := f.input(t, "1")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		task, _ := f.h.tasks.Find("owner", first.ID)
		if task.State == request.StateCancelled {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if task, _ := f.h.tasks.Find("owner", first.ID); task.State != request.StateCancelled {
		t.Fatal("original session not interrupted")
	}
	f.send(t, confirmID, "1")
	if _, ok := f.h.tasks.Active("owner", "", other.ID); !ok {
		t.Fatal("old confirmation interrupted other session")
	}
	f.send(t, rejectedID, "不要偷偷执行这条追加指令")
	if len(f.h.tasks.List("owner")) != 2 {
		t.Fatal("rejected message later became work")
	}
	select {
	case <-started:
		t.Fatal("rejected input executed")
	default:
	}
	// 持久回执在重新打开状态后仍阻止原来源。
	otherTask, _ := f.h.tasks.Active("owner", "", other.ID)
	f.h.coordinator.Cancel("owner", otherTask.ID)
	deadline = time.Now().Add(3 * time.Second)
	for f.h.tasks.Status("owner").Running > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	reopened, err := request.NewStore(f.h.tasks.Root())
	if err != nil {
		t.Fatal(err)
	}
	receipts := 0
	for _, r := range f.h.tasks.List("owner") {
		if strings.Contains(r.Summary, "追加") {
			t.Fatal("rejected payload stored")
		}
	}
	for _, id := range []int64{rejectedID} {
		source := sourceForFixture(t, f, id)
		if _, ok := reopened.FindRejection(source); ok {
			receipts++
		}
	}
	if receipts != 1 {
		t.Fatal("rejection did not survive restart")
	}
}

func sourceForFixture(t *testing.T, f *numberMenuFixture, id int64) string {
	t.Helper()
	source, err := sourceMessageKey(f.client, ilink.WeixinMessage{MessageID: id, FromUserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	return source
}
