package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// 前端靠 event.done 解除"测速中"，所以必须钉住实际序列化出来的字段名。
// 如果这里改成 Done/IsDone 或加错 tag，前端会静默收不到终态而卡死。
func TestTunnelStatusDTOExposesDoneField(t *testing.T) {
	raw, err := json.Marshal(TunnelStatusDTO{
		Status:  "connected",
		PingMS:  15,
		Message: "测速超时，点击重试",
		Done:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{`"status":"connected"`, `"pingMs":15`, `"done":true`, `"message":`} {
		if !strings.Contains(text, want) {
			t.Fatalf("序列化结果缺少 %s，实际 %s", want, text)
		}
	}
}

// 超时标记必须能传到前端，否则前端无法区分"超时"和"普通失败"。
func TestTunnelStatusDTOExposesTimeoutField(t *testing.T) {
	raw, err := json.Marshal(TunnelStatusDTO{
		Status:  "error",
		Message: "测速超时，点击重试",
		Done:    true,
		Timeout: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"timeout":true`) {
		t.Fatalf("序列化结果应含 timeout:true，实际 %s", string(raw))
	}
}

// 未结束的中间样本不应带 done，否则前端会在测速未跑完时解除"测速中"。
func TestTunnelStatusDTOOmitsDoneForIntermediateSamples(t *testing.T) {
	raw, err := json.Marshal(TunnelStatusDTO{Status: "connected", PingMS: 15})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"done"`) || strings.Contains(string(raw), `"timeout"`) {
		t.Fatalf("中间样本不应带 done/timeout 字段，实际 %s", string(raw))
	}
}

// 前端靠 level 决定高亮颜色、靠 title/hint 渲染提示框，所以钉住线格式。
func TestDiagnosisDTOWireFormat(t *testing.T) {
	raw, err := json.Marshal(DiagnosisDTO{
		Level: "warning",
		Title: "该端口已被占用",
		Hint:  "请退出其他设备上的连接后重试",
		Raw:   "start proxy error: port already in use",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{`"level":"warning"`, `"title":"该端口已被占用"`, `"hint":`, `"raw":`} {
		if !strings.Contains(text, want) {
			t.Fatalf("序列化结果缺少 %s，实际 %s", want, text)
		}
	}
}

// 认不出来的错误只带标题与原始信息，不应凭空生成解决建议。
func TestDiagnosisDTOOmitsEmptyHint(t *testing.T) {
	raw, err := json.Marshal(DiagnosisDTO{Level: "error", Title: "连接失败", Raw: "some odd failure"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"hint"`) {
		t.Fatalf("没有建议时不应带 hint 字段，实际 %s", string(raw))
	}
}
