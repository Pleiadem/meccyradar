package frpengine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"

	frpclient "github.com/fatedier/frp/client"
	"github.com/fatedier/frp/pkg/config/source"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/policy/security"
	frplog "github.com/fatedier/frp/pkg/util/log"
	goliblog "github.com/fatedier/golib/log"
	"meccyradar/internal/auth"
)

var (
	ErrAlreadyRunning = errors.New("frp engine is already running")
	ErrNotRunning     = errors.New("frp engine is not running")
)

type LogFunc func(line string)
type ExitFunc func(err error)

type Engine struct {
	mu sync.Mutex

	config auth.FRPConfig
	logs   LogFunc
	onExit ExitFunc

	service *frpclient.Service
	cancel  context.CancelFunc
	done    chan struct{}
	running bool
	err     error
}

func New(config auth.FRPConfig, logs LogFunc) (*Engine, error) {
	return NewWithExit(config, logs, nil)
}

func NewWithExit(config auth.FRPConfig, logs LogFunc, onExit ExitFunc) (*Engine, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	return &Engine{config: config, logs: logs, onExit: onExit}, nil
}

func (e *Engine) Start() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running {
		return ErrAlreadyRunning
	}
	common, proxies, err := buildConfig(e.config)
	if err != nil {
		return err
	}
	configSource := source.NewConfigSource()
	if err := configSource.ReplaceAll(proxies, nil); err != nil {
		return fmt.Errorf("load FRP proxy configuration: %w", err)
	}
	service, err := frpclient.NewService(frpclient.ServiceOptions{
		Common:                 common,
		ConfigSourceAggregator: source.NewAggregator(configSource),
		UnsafeFeatures:         &security.UnsafeFeatures{},
	})
	if err != nil {
		return fmt.Errorf("create FRP service: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	e.service = service
	e.cancel = cancel
	e.done = make(chan struct{})
	e.running = true
	e.err = nil
	previousLogger := frplog.Logger
	frplog.Logger = goliblog.New(
		goliblog.WithOutput(logWriter{callback: e.logs, secrets: []string{e.config.ServerAddr, e.config.Token, e.config.User}}),
		goliblog.WithLevel(goliblog.InfoLevel),
		goliblog.WithCaller(false),
	)
	go func() {
		runErr := service.Run(ctx)
		e.mu.Lock()
		e.err = runErr
		e.running = false
		e.service = nil
		e.cancel = nil
		e.mu.Unlock()
		frplog.Logger = previousLogger
		close(e.done)
		if runErr != nil && !errors.Is(runErr, context.Canceled) && e.onExit != nil {
			e.onExit(runErr)
		}
	}()
	return nil
}

func (e *Engine) Stop() error {
	e.mu.Lock()
	if !e.running {
		e.mu.Unlock()
		return ErrNotRunning
	}
	cancel := e.cancel
	done := e.done
	e.mu.Unlock()
	cancel()
	<-done
	return nil
}

func (e *Engine) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

func (e *Engine) LastError() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}

type logWriter struct {
	callback LogFunc
	secrets  []string
}

func (w logWriter) Write(p []byte) (int, error) {
	if w.callback != nil {
		w.callback(redactLogLine(strings.TrimRight(string(p), "\r\n"), w.secrets))
	}
	return len(p), nil
}

func redactLogLine(line string, secrets []string) string {
	for _, secret := range secrets {
		if strings.TrimSpace(secret) != "" {
			line = strings.ReplaceAll(line, secret, "[REDACTED]")
		}
	}
	return line
}

func validateConfig(config auth.FRPConfig) error {
	if strings.TrimSpace(config.ServerAddr) == "" {
		return errors.New("FRP server address is required")
	}
	if net.ParseIP(config.ServerAddr) == nil && strings.ContainsAny(config.ServerAddr, "/\\ ") {
		return errors.New("FRP server address is invalid")
	}
	if config.ServerPort < 1 || config.ServerPort > 65535 {
		return errors.New("FRP server port must be between 1 and 65535")
	}
	if strings.TrimSpace(config.Token) == "" {
		return errors.New("FRP token is required")
	}
	if len(config.Proxies) == 0 {
		return errors.New("at least one FRP proxy is required")
	}
	seen := make(map[string]struct{}, len(config.Proxies))
	for _, proxy := range config.Proxies {
		if _, ok := seen[proxy.Name]; ok || strings.TrimSpace(proxy.Name) == "" {
			return errors.New("FRP proxy names must be unique and non-empty")
		}
		seen[proxy.Name] = struct{}{}
		if proxy.LocalPort < 1 || proxy.LocalPort > 65535 {
			return fmt.Errorf("proxy %q local port must be between 1 and 65535", proxy.Name)
		}
		switch proxy.Type {
		case "tcp":
			if proxy.RemotePort < 1 || proxy.RemotePort > 65535 {
				return fmt.Errorf("proxy %q remote port must be between 1 and 65535", proxy.Name)
			}
		case "http":
			if len(proxy.CustomDomains) == 0 && strings.TrimSpace(proxy.Subdomain) == "" {
				return fmt.Errorf("proxy %q HTTP requires customDomains or subdomain", proxy.Name)
			}
		default:
			return fmt.Errorf("proxy %q uses unsupported type %q", proxy.Name, proxy.Type)
		}
	}
	return nil
}

func buildConfig(config auth.FRPConfig) (*v1.ClientCommonConfig, []v1.ProxyConfigurer, error) {
	if err := validateConfig(config); err != nil {
		return nil, nil, err
	}
	loginFailExit := true
	common := &v1.ClientCommonConfig{
		ServerAddr:    config.ServerAddr,
		ServerPort:    config.ServerPort,
		User:          config.User,
		LoginFailExit: &loginFailExit,
		Auth: v1.AuthClientConfig{
			Method: v1.AuthMethodToken,
			Token:  config.Token,
		},
	}
	proxies := make([]v1.ProxyConfigurer, 0, len(config.Proxies))
	for _, proxy := range config.Proxies {
		base := v1.ProxyBaseConfig{
			Name: proxy.Name,
			Type: proxy.Type,
			ProxyBackend: v1.ProxyBackend{
				LocalIP:   proxy.LocalIP,
				LocalPort: proxy.LocalPort,
			},
		}
		if proxy.Type == "tcp" {
			proxies = append(proxies, &v1.TCPProxyConfig{ProxyBaseConfig: base, RemotePort: proxy.RemotePort})
		} else {
			proxies = append(proxies, &v1.HTTPProxyConfig{
				ProxyBaseConfig: base,
				DomainConfig:    v1.DomainConfig{CustomDomains: append([]string(nil), proxy.CustomDomains...), SubDomain: proxy.Subdomain},
			})
		}
	}
	return common, proxies, nil
}

// BuildConfigForTest exposes the in-memory conversion without starting a network connection.
func BuildConfigForTest(config auth.FRPConfig) (*v1.ClientCommonConfig, []v1.ProxyConfigurer, error) {
	return buildConfig(config)
}
