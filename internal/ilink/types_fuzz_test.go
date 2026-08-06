package ilink

import (
	"encoding/json"
	"testing"
)

func FuzzDecodeGetUpdatesResponse(f *testing.F) {
	f.Add([]byte(`{"ret":0,"msgs":[],"get_updates_buf":"cursor"}`))
	f.Add([]byte(`{"ret":0,"msgs":[{"from_user_id":"user","message_type":1,"message_state":2,"item_list":[{"type":1,"text_item":{"text":"你好"}}]}]}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"msgs":"invalid"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		var response GetUpdatesResponse
		if err := json.Unmarshal(data, &response); err != nil {
			return
		}
		// 日志摘要属于反序列化后的首个消费点，一并覆盖异常文本和组合结构。
		limit := len(response.Msgs)
		if limit > 64 {
			limit = 64
		}
		for index := 0; index < limit; index++ {
			_ = FormatMessageSummary(response.Msgs[index])
		}
	})
}
