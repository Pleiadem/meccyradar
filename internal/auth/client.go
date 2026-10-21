package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var ErrCardAlreadyOnline = errors.New("该卡密已在线")
var ErrInvalidResponse = errors.New("服务端返回的数据无法解析")

type Client struct {
	baseURL string
	http    *http.Client
}

type Session struct {
	LeaseID           string `json:"leaseId"`
	HeartbeatInterval int    `json:"heartbeatInterval"`
	LeaseExpireAtUnix int64  `json:"leaseExpireAtUnix"`
}

type Proxy struct {
	Name          string   `json:"name"`
	Type          string   `json:"type"`
	LocalIP       string   `json:"localIP"`
	LocalPort     int      `json:"localPort"`
	RemotePort    int      `json:"remotePort"`
	CustomDomains []string `json:"customDomains,omitempty"`
	Subdomain     string   `json:"subdomain,omitempty"`
}

type FRPConfig struct {
	ServerAddr string            `json:"serverAddr"`
	ServerPort int               `json:"serverPort"`
	User       string            `json:"user"`
	Token      string            `json:"token"`
	Meta       map[string]string `json:"meta,omitempty"`
	Proxies    []Proxy           `json:"proxies"`
}

type AuthData struct {
	AppName      string    `json:"appName"`
	ShareBaseURL string    `json:"shareBaseUrl"`
	ExpireAtUnix int64     `json:"expireAtUnix"`
	ExpireAtStr  string    `json:"expireAtStr"`
	Session      Session   `json:"session"`
	FRPConfig    FRPConfig `json:"frpConfig"`
}

type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

type loginRequest struct {
	CardKey string `json:"cardKey"`
}
type leaseRequest struct {
	LeaseID string `json:"leaseId"`
}

type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

// plainHTTPAllowedHosts 是允许明文 HTTP 的主机白名单。
//
// 默认规则是"非本机必须 HTTPS"：登录请求会带卡密，心跳会带租约 ID，
// 明文传输等于把这些凭据暴露在链路上。这里只放行运维明确指定、且由
// 我们自己控制的自建管理端。加条目之前先确认该链路是否可能被旁路监听，
// 不要图省事往里塞域名。
var plainHTTPAllowedHosts = map[string]bool{
	"43.138.163.188": true,
}

// isPlainHTTPAllowedHost 判断该主机是否允许走明文 HTTP。
func isPlainHTTPAllowedHost(host string) bool {
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return plainHTTPAllowedHosts[host]
}

func NewClient(rawBaseURL string, httpClient *http.Client) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(rawBaseURL), "/"))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, errors.New("服务端地址必须包含协议和主机名")
	}
	if base.Scheme != "https" && !isPlainHTTPAllowedHost(base.Hostname()) {
		return nil, errors.New("非本机地址的服务端必须使用 HTTPS")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{baseURL: base.String(), http: httpClient}, nil
}

func (c *Client) Login(ctx context.Context, cardKey string) (AuthData, error) {
	if strings.TrimSpace(cardKey) == "" {
		return AuthData{}, errors.New("请输入卡密")
	}
	var out AuthData
	if err := c.doJSON(ctx, http.MethodPost, "/api/client/login", loginRequest{CardKey: cardKey}, &out); err != nil {
		return AuthData{}, err
	}
	if err := validateAuthData(out); err != nil {
		return AuthData{}, err
	}
	return out, nil
}

func (c *Client) Heartbeat(ctx context.Context, leaseID string) error {
	if strings.TrimSpace(leaseID) == "" {
		return errors.New("缺少租约 ID")
	}
	return c.doJSON(ctx, http.MethodPost, "/api/client/heartbeat", leaseRequest{LeaseID: leaseID}, &struct{}{})
}

func (c *Client) Logout(ctx context.Context, leaseID string) error {
	if strings.TrimSpace(leaseID) == "" {
		return nil
	}
	return c.doJSON(ctx, http.MethodPost, "/api/client/logout", leaseRequest{LeaseID: leaseID}, &struct{}{})
}

func (c *Client) doJSON(ctx context.Context, method, path string, requestBody any, responseBody any) error {
	body, err := json.Marshal(requestBody)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		// url 解析失败的错误里可能带完整地址
		log.Printf("management request build failed (base=%s): %v", c.baseURL, err)
		return errors.New("服务端地址无效，请检查配置")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return c.redactTransportError(err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		log.Printf("management response read failed: %v", err)
		return errors.New("服务端响应读取失败，请重试")
	}
	if resp.StatusCode == http.StatusConflict {
		return &ConflictError{Message: "当前卡密已在其他设备运行，请先退出原设备"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError(resp.StatusCode, payload)
	}
	var envelope envelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return ErrInvalidResponse
	}
	if envelope.Code != 200 {
		return applicationError(envelope.Code, envelope.Msg)
	}
	if responseBody == nil || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(envelope.Data, responseBody); err != nil {
		return ErrInvalidResponse
	}
	return nil
}

// clientFacingStatus 是客户端会遇到的业务状态码及其兜底文案。
//
// 这些文案与服务端 MSG_* 常量保持一致：服务端在这些状态码上返回的是
// 受控业务消息，客户端优先展示它；拿不到（或拿到的是技术文本）时用这里的兜底。
var clientFacingStatus = map[int]string{
	http.StatusUnauthorized:       "卡密不存在或格式不正确",
	http.StatusForbidden:          "卡密已被禁用",
	http.StatusNotFound:           "卡密不存在",
	http.StatusConflict:           "该卡密已在其他设备上运行，请先退出原设备",
	http.StatusTooManyRequests:    "操作过于频繁，请稍后重试",
	http.StatusBadGateway:         "服务资源分配失败，请稍后重试",
	http.StatusServiceUnavailable: "暂无可用服务器，请稍后重试",
}

// statusError 把非 2xx 响应转成用户能看懂的中文提示。
//
// 不能直接抛 resp.Status：那是 "401 Unauthorized" 这种 HTTP 术语，
// 用户看到「服务端请求失败（HTTP 401 Forbidden）」只会一头雾水，
// 既不知道是卡密问题，也不知道该做什么。
func statusError(status int, payload []byte) error {
	if message, ok := businessMessage(payload); ok {
		if _, known := clientFacingStatus[status]; known {
			return errors.New(message)
		}
	}
	if fallback, known := clientFacingStatus[status]; known {
		return errors.New(fallback)
	}
	return errors.New("连接失败，请检查网络后重试")
}

// businessMessage 从 FastAPI 的 {"detail": "..."} 里取出可展示的业务文案。
//
// 只接受"像业务文案"的内容：业务文案是中文短句，而框架/异常文本
// （"Internal Server Error"、"lease invalid"、带堆栈的字符串）是英文或
// 含 URL，一律拒绝 —— 这类内容展示给用户既看不懂，还可能泄露内部细节。
func businessMessage(payload []byte) (string, bool) {
	var body struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return "", false
	}
	message := strings.TrimSpace(body.Detail)
	if message == "" || len([]rune(message)) > 60 {
		return "", false
	}
	if strings.Contains(message, "://") || strings.Contains(message, "Traceback") || strings.Contains(message, "Exception") {
		return "", false
	}
	for _, r := range message {
		if r > unicode.MaxASCII {
			return message, true
		}
	}
	// 纯 ASCII：视为技术文本，交给兜底文案
	return "", false
}

// redactTransportError 把底层网络错误转成用户可读的提示。
//
// net/http 的错误里带完整 URL（Post "http://host:port/path": dial tcp ...），
// 直接返回给用户就泄露了服务端地址。完整错误只进日志，方便排查。
func (c *Client) redactTransportError(err error) error {
	log.Printf("management request to %s failed: %v", c.baseURL, err)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("连接服务端超时，请检查网络后重试")
	case errors.Is(err, context.Canceled):
		return errors.New("请求已取消")
	default:
		return errors.New("无法连接服务端，请检查网络或服务端地址")
	}
}

func applicationError(code int, msg string) error {
	if code == http.StatusConflict {
		return &ConflictError{Message: "当前卡密已在其他设备运行，请先退出原设备"}
	}
	if msg == "" {
		msg = "management request rejected with code " + strconv.Itoa(code)
	}
	return fmt.Errorf("服务端拒绝了请求：%s", msg)
}

func validateAuthData(data AuthData) error {
	if data.AppName == "" || data.ShareBaseURL == "" || data.ExpireAtUnix <= 0 || data.Session.LeaseID == "" || data.FRPConfig.ServerAddr == "" || data.FRPConfig.ServerPort <= 0 || data.FRPConfig.Token == "" || len(data.FRPConfig.Proxies) == 0 {
		return ErrInvalidResponse
	}
	for _, proxy := range data.FRPConfig.Proxies {
		if proxy.Name == "" || proxy.Type == "" || proxy.LocalPort <= 0 || proxy.LocalPort > 65535 {
			return ErrInvalidResponse
		}
		if proxy.Type == "tcp" && (proxy.RemotePort <= 0 || proxy.RemotePort > 65535) {
			return ErrInvalidResponse
		}
		if proxy.Type == "http" && len(proxy.CustomDomains) == 0 && proxy.Subdomain == "" {
			return ErrInvalidResponse
		}
	}
	return nil
}
