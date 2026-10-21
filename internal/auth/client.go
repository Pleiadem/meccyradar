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
		return fmt.Errorf("服务端请求失败（HTTP %s）", resp.Status)
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
