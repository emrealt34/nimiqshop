// Command healthcheck is the container HEALTHCHECK probe: the runtime image
// is distroless (no shell, no curl), so a tiny static binary asks the
// running server for /api/health and exits 0 only when it answers ok.
//
//	healthcheck            # probes LISTEN_ADDR (default :8084) on loopback
//	healthcheck -addr :9000
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"nimiqshop/internal/loopback"
)

func main() {
	addr := flag.String("addr", envOr("LISTEN_ADDR", ":8084"), "server listen address (host:port or :port)")
	timeout := flag.Duration("timeout", 3*time.Second, "probe timeout")
	flag.Parse()

	if err := probe(*addr, *timeout); err != nil {
		fmt.Fprintln(os.Stderr, "unhealthy:", err)
		os.Exit(1)
	}
	fmt.Println("ok")
}

// probe performs one GET /api/health against addr and checks the JSON body.
func probe(addr string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+hostPort(addr)+"/api/health", nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	var body struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if !body.OK {
		return fmt.Errorf("server reports ok=false")
	}
	return nil
}

// hostPort turns a listen address into a dialable host:port. A bare ":8084"
// (listen on every interface) is probed through the loopback interface.
func hostPort(addr string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil || port == "" {
		return loopback.HostPort("8084")
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return loopback.HostPort(port)
	}
	return net.JoinHostPort(host, port)
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
