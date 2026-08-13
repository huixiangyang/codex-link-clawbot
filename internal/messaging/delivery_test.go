package messaging

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/preference"
	"github.com/huixiangyang/codex-link-clawbot/internal/taskqueue"
	"github.com/huixiangyang/codex-link-clawbot/internal/visual"
)

func TestDeliveryReportDistinguishesExplicitFailureAndAmbiguousPartialDelivery(t *testing.T) {
	handler := NewHandler(nil)
	explicitServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ret":1,"errmsg":"rejected"}`))
	}))
	defer explicitServer.Close()
	explicitClient := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot", ILinkUserID: "owner", BaseURL: explicitServer.URL})
	message := ilink.WeixinMessage{FromUserID: "owner", ContextToken: "context"}
	report := handler.deliverReplyPlan(
		context.Background(), explicitClient, message, "回答", nil, nil, nil, "client-explicit", DeliverySource{},
		preference.ResponseAdaptive, visual.StyleEditorial, "Project",
	)
	if report.Outcome != taskqueue.DeliveryExplicitFailure || report.TextSent || report.MediaSent != 0 {
		t.Fatalf("explicit report = %#v", report)
	}

	successServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer successServer.Close()
	successClient := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot", ILinkUserID: "owner", BaseURL: successServer.URL})
	report = handler.deliverReplyPlan(
		context.Background(), successClient, message, "回答", nil, nil, []string{"http://127.0.0.1:1/unavailable.png"}, "client-partial", DeliverySource{},
		preference.ResponseAdaptive, visual.StyleEditorial, "Project",
	)
	if report.Outcome != taskqueue.DeliveryAmbiguous || !report.TextSent || report.Failure != taskqueue.ReasonDeliveryAmbiguous {
		t.Fatalf("partial report = %#v", report)
	}
}
