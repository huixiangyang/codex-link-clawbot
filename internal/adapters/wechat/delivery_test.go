package wechat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
)

func TestDeliveryReportDistinguishesExplicitFailureAndAmbiguousPartialDelivery(t *testing.T) {
	handler := newBareHandler(nil)
	explicitServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ret":1,"errmsg":"rejected"}`))
	}))
	defer explicitServer.Close()
	explicitClient := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot", ILinkUserID: "owner", BaseURL: explicitServer.URL})
	message := ilink.WeixinMessage{FromUserID: "owner", ContextToken: "context"}
	report := handler.deliverReplyPlan(
		context.Background(), explicitClient, message, "回答", nil, nil, "client-explicit",
		presentation.ResponseAdaptive, presentation.StyleEditorial,
	)
	if report.Outcome != request.DeliveryExplicitFailure || report.TextSent || report.MediaSent != 0 {
		t.Fatalf("explicit report = %#v", report)
	}

	successServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer successServer.Close()
	successClient := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot", ILinkUserID: "owner", BaseURL: successServer.URL})
	report = handler.deliverReplyPlan(
		context.Background(), successClient, message, "回答", nil, []string{"http://127.0.0.1:1/unavailable.png"}, "client-partial",
		presentation.ResponseAdaptive, presentation.StyleEditorial,
	)
	if report.Outcome != request.DeliveryAmbiguous || !report.TextSent || report.Failure != request.ReasonDeliveryAmbiguous {
		t.Fatalf("partial report = %#v", report)
	}
}

func TestFullTextChunksKeepIdentityAndPartialFailure(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var payload ilink.SendMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		text := payload.Msg.ItemList[0].TextItem.Text
		if len([]rune(text)) > 1200 || !strings.Contains(text, "abcdef12") {
			t.Error("chunk is too long or lost source identity")
		}
		if calls == 2 {
			_, _ = w.Write([]byte(`{"ret":1}`))
		} else {
			_, _ = w.Write([]byte(`{"ret":0}`))
		}
	}))
	defer server.Close()
	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot", ILinkUserID: "owner", BaseURL: server.URL})
	report := sendResultText(context.Background(), client, "owner", "结果 abcdef12 · 主项目\n"+strings.Repeat("正文", 1500), "context")
	if report.Outcome != request.DeliveryAmbiguous || !report.TextSent || calls != 2 {
		t.Fatalf("partial text report = %+v, calls=%d", report, calls)
	}
}
