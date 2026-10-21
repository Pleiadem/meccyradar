package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func loginAgainst(t *testing.T, status int, body string) error {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Login(context.Background(), "CARD-X")
	return err
}

// 用户可见的错误里绝不能出现 HTTP 状态术语。
func TestStatusErrorsNeverLeakHTTPTerms(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"401 无效卡密", http.StatusUnauthorized, `{"detail":"卡密不存在或格式不正确"}`, "卡密不存在或格式不正确"},
		{"403 已禁用", http.StatusForbidden, `{"detail":"卡密已被禁用"}`, "卡密已被禁用"},
		{"403 已过期", http.StatusForbidden, `{"detail":"卡密已过期"}`, "卡密已过期"},
		{"503 无可用服务器", http.StatusServiceUnavailable, `{"detail":"暂无可用服务器，请稍后重试"}`, "暂无可用服务器，请稍后重试"},
		{"502 分配失败", http.StatusBadGateway, `{"detail":"服务资源分配失败，请稍后重试"}`, "服务资源分配失败，请稍后重试"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := loginAgainst(t, testCase.status, testCase.body)
			if err == nil {
				t.Fatal("期望报错")
			}
			message := err.Error()
			if message != testCase.want {
				t.Fatalf("期望 %q，实际 %q", testCase.want, message)
			}
			assertNoTechnicalTerms(t, message)
		})
	}
}

// 服务端没给 detail（或给的是技术文本）时必须用兜底文案，而不是把
// "401 Unauthorized" / "Internal Server Error" 抛给用户。
func TestStatusErrorsFallBackWhenDetailIsTechnical(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"无 body", http.StatusUnauthorized, ``, "卡密不存在或格式不正确"},
		{"英文技术文本", http.StatusUnauthorized, `{"detail":"lease invalid"}`, "卡密不存在或格式不正确"},
		{"HTML 错误页", http.StatusUnauthorized, `<html>401</html>`, "卡密不存在或格式不正确"},
		{"未知状态码", http.StatusInternalServerError, `{"detail":"Internal Server Error"}`, "连接失败，请检查网络后重试"},
		{"带堆栈", http.StatusForbidden, `{"detail":"Traceback (most recent call last)"}`, "卡密已被禁用"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := loginAgainst(t, testCase.status, testCase.body)
			if err == nil {
				t.Fatal("期望报错")
			}
			message := err.Error()
			if message != testCase.want {
				t.Fatalf("期望 %q，实际 %q", testCase.want, message)
			}
			assertNoTechnicalTerms(t, message)
		})
	}
}

// 409 仍然走 ConflictError，文案不变。
func TestConflictStillMapped(t *testing.T) {
	err := loginAgainst(t, http.StatusConflict, `{"detail":"card already online"}`)
	var conflictErr *ConflictError
	if !errors.As(err, &conflictErr) {
		t.Fatalf("409 应映射为 ConflictError，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "已在其他设备") {
		t.Fatalf("文案应说明已被占用，实际 %q", err.Error())
	}
}

func assertNoTechnicalTerms(t *testing.T, message string) {
	t.Helper()
	for _, term := range []string{"HTTP", "Forbidden", "Unauthorized", "Internal Server Error", "http://", "https://", "Traceback"} {
		if strings.Contains(message, term) {
			t.Fatalf("用户可见文案不得包含技术术语 %q：%s", term, message)
		}
	}
}
