package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var ErrCardAlreadyOnline = errors.New("该卡密已在线")
var ErrInvalidResponse = errors.New("管理端返回的数据无法解析")

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

func NewClient(rawBaseURL string, httpClient *http.Client) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(rawBaseURL), "/"))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, errors.New("管理端地址必须包含协议和主机名")
	}
	if base.Scheme != "https" && base.Hostname() != "127.0.0.1" && base.Hostname() != "localhost" {
		return nil, errors.New("非本机地址的管理端必须使用 HTTPS")
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
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusConflict {
		return &ConflictError{Message: "当前卡密已在其他设备运行，请先退出原设备"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("管理端请求失败（HTTP %s）", resp.Status)
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

func applicationError(code int, msg string) error {
	if code == http.StatusConflict {
		return &ConflictError{Message: "当前卡密已在其他设备运行，请先退出原设备"}
	}
	if msg == "" {
		msg = "management request rejected with code " + strconv.Itoa(code)
	}
	return fmt.Errorf("管理端拒绝了请求：%s", msg)
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
