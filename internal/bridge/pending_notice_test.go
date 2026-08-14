package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/delivery"
	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
)

func TestPendingNoticeStorePersistsDeduplicatesAndCompletes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending-notices.json")
	now := time.Unix(1000, 0)
	store, err := delivery.OpenNoticeStore(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	input := delivery.NoticeInput{
		Kind: delivery.NoticeTaskRecovery, DedupKey: "task:daily:slot", ReferenceID: "daily",
		Title: "结果待取回", Body: "打开请求队列", TTL: 24 * time.Hour,
	}
	first, duplicate, err := store.Enqueue("owner", input)
	if err != nil || duplicate || first.ID == "" {
		t.Fatalf("first enqueue = %#v, %v, %v", first, duplicate, err)
	}
	second, duplicate, err := store.Enqueue("owner", input)
	if err != nil || !duplicate || second.ID != first.ID {
		t.Fatalf("duplicate enqueue = %#v, %v, %v", second, duplicate, err)
	}
	reopened, err := delivery.OpenNoticeStore(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	notices, err := reopened.List("owner", 10)
	if err != nil || len(notices) != 1 || notices[0].ReferenceID != "daily" {
		t.Fatalf("persisted notices = %#v, %v", notices, err)
	}
	if err := reopened.Complete("owner", []string{first.ID}); err != nil {
		t.Fatal(err)
	}
	if notices, err := reopened.List("owner", 10); err != nil || len(notices) != 0 {
		t.Fatalf("completed notices = %#v, %v", notices, err)
	}
}

func TestPendingNoticesFlushWithCurrentInboundContext(t *testing.T) {
	store, err := delivery.OpenNoticeStore(filepath.Join(t.TempDir(), "pending-notices.json"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Enqueue("owner", delivery.NoticeInput{
		Kind: delivery.NoticeDeployment, DedupKey: "deploy:v3", Title: "部署完成", Body: "版本已更新", TTL: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	var contextToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var payload ilink.SendMessageRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		contextToken = payload.Msg.ContextToken
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()
	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot", ILinkUserID: "owner", BaseURL: server.URL})
	handler := newBareHandler(nil)
	handler.pendingNotices = store
	handler.flushPendingNotices(context.Background(), client, "owner", "fresh-context")
	if contextToken != "fresh-context" {
		t.Fatalf("pending notice context token = %q", contextToken)
	}
	if notices, err := store.List("owner", 10); err != nil || len(notices) != 0 {
		t.Fatalf("delivered notices = %#v, %v", notices, err)
	}
}

func TestPendingNoticeDefiniteRejectionRemainsDeferred(t *testing.T) {
	store, err := delivery.OpenNoticeStore(filepath.Join(t.TempDir(), "pending-notices.json"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Enqueue("owner", delivery.NoticeInput{
		Kind: delivery.NoticeTaskRecovery, DedupKey: "task:one", Title: "结果待取回", Body: "打开请求队列", TTL: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ret":-2,"errmsg":"prepare failed"}`))
	}))
	defer server.Close()
	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot", ILinkUserID: "owner", BaseURL: server.URL})
	handler := newBareHandler(nil)
	handler.pendingNotices = store
	handler.flushPendingNotices(context.Background(), client, "owner", "expired-context")
	if notices, err := store.List("owner", 10); err != nil || len(notices) != 1 {
		t.Fatalf("rejected notices = %#v, %v", notices, err)
	}
}

func TestPendingNoticeStoreExpiresAndRejectsUnsafeInput(t *testing.T) {
	now := time.Unix(1000, 0)
	store, err := delivery.OpenNoticeStore(filepath.Join(t.TempDir(), "pending-notices.json"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	notice, _, err := store.Enqueue("owner", delivery.NoticeInput{
		Kind: delivery.NoticeDeployment, DedupKey: "deploy:v2", Title: "部署完成", Body: "版本已更新", TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if notices, err := store.List("owner", 10); err != nil || len(notices) != 0 {
		t.Fatalf("expired notices = %#v, %v", notices, err)
	}
	if _, _, err := store.Enqueue("owner", delivery.NoticeInput{
		Kind: delivery.NoticeDeployment, DedupKey: "unsafe\nkey", Title: "部署完成", Body: strings.Repeat("测", 6001), TTL: time.Hour,
	}); err == nil {
		t.Fatal("unsafe pending notice was accepted")
	}
	if err := store.Complete("owner", []string{notice.ID}); err != nil {
		t.Fatal(err)
	}
}
