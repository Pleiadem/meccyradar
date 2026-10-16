package frpengine

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"meccyradar/internal/auth"
)

func TestRealFRPDataPlaneTCPAndHTTP(t *testing.T) {
	frpsBin := os.Getenv("MECCYRADAR_FRPS_BIN")
	if frpsBin == "" {
		t.Skip("set MECCYRADAR_FRPS_BIN to run the real FRPS data-plane test")
	}
	const (
		bindPort   = 17000
		tcpLocal   = 19001
		tcpRemote  = 19002
		httpLocal  = 19003
		vhostPort  = 18080
		authToken  = "integration-token"
		testDomain = "radar.test"
	)

	stopTCP := startTCPEcho(t, tcpLocal)
	defer stopTCP()
	stopHTTP := startHTTPServer(t, httpLocal)
	defer stopHTTP()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frps := exec.CommandContext(ctx, frpsBin,
		"--bind-addr", "127.0.0.1",
		"--bind-port", strconv.Itoa(bindPort),
		"--proxy-bind-addr", "127.0.0.1",
		"--vhost-http-port", strconv.Itoa(vhostPort),
		"--token", authToken,
		"--log-file", "console",
		"--disable-log-color",
	)
	frps.Stdout = io.Discard
	frps.Stderr = io.Discard
	if err := frps.Start(); err != nil {
		t.Fatalf("start frps: %v", err)
	}
	t.Cleanup(func() {
		_ = frps.Process.Kill()
		_ = frps.Wait()
	})
	waitForPort(t, "127.0.0.1:"+strconv.Itoa(bindPort))

	engine, err := New(auth.FRPConfig{
		ServerAddr: "127.0.0.1",
		ServerPort: bindPort,
		Token:      authToken,
		Proxies: []auth.Proxy{
			{Name: "radar_tcp", Type: "tcp", LocalIP: "127.0.0.1", LocalPort: tcpLocal, RemotePort: tcpRemote},
			{Name: "radar_http", Type: "http", LocalIP: "127.0.0.1", LocalPort: httpLocal, CustomDomains: []string{testDomain}},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = engine.Stop() }()

	waitForPort(t, "127.0.0.1:"+strconv.Itoa(tcpRemote))
	assertTCPEcho(t, tcpRemote)
	assertHTTPProxy(t, vhostPort, testDomain)
	if !engine.Running() {
		t.Fatal("engine stopped before data-plane assertions completed")
	}
}

func startTCPEcho(t *testing.T, port int) func() {
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("listen tcp echo: %v", err)
	}
	stop := make(chan struct{})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				select {
				case <-stop:
					return
				default:
				}
				continue
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return func() { close(stop); _ = listener.Close() }
}

func startHTTPServer(t *testing.T, port int) func() {
	server := &http.Server{Addr: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "meccyradar-http-ok")
	})}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		t.Fatalf("listen http: %v", err)
	}
	go func() { _ = server.Serve(listener) }()
	return func() {
		_ = server.Close()
	}
}

func waitForPort(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("port did not become available: %s", address)
}

func assertTCPEcho(t *testing.T, port int) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 2*time.Second)
	if err != nil {
		t.Fatalf("dial TCP proxy: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("meccyradar-tcp")); err != nil {
		t.Fatalf("write TCP proxy: %v", err)
	}
	got := make([]byte, len("meccyradar-tcp"))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read TCP proxy: %v", err)
	}
	if string(got) != "meccyradar-tcp" {
		t.Fatalf("unexpected TCP response: %q", got)
	}
}

func assertHTTPProxy(t *testing.T, port int, host string) {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second}
	var response *http.Response
	var err error
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		req, reqErr := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/", nil)
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		req.Host = host
		response, err = client.Do(req)
		if err == nil && response.StatusCode == http.StatusOK {
			break
		}
		if response != nil {
			_ = response.Body.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("HTTP proxy request: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(bufio.NewReader(response.Body))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "meccyradar-http-ok") {
		t.Fatalf("unexpected HTTP proxy response: status=%d body=%q", response.StatusCode, body)
	}
}
