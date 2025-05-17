package lib

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// StatusProvider is an interface for checking if the upstream service is available
type StatusProvider interface {
	IsRunning() bool
}

// Proxy represents an HTTP proxy with configurable upstream
type Proxy struct {
	targetAddr string
	status     StatusProvider
	proxy      *httputil.ReverseProxy
}

// New creates a new proxy instance
func New(targetAddr string, status StatusProvider) (*Proxy, error) {
	p := &Proxy{
		targetAddr: targetAddr,
		status:     status,
	}

	if err := p.setupProxy(); err != nil {
		return nil, err
	}

	return p, nil
}

// setupProxy configures the reverse proxy based on the target address
func (p *Proxy) setupProxy() error {
	var targetURL string
	if strings.HasPrefix(p.targetAddr, "unix:") {
		// For Unix domain sockets, we use a special URL scheme
		targetURL = fmt.Sprintf("http://unix")
	} else {
		targetURL = fmt.Sprintf("http://%s", p.targetAddr)
	}

	target, err := url.Parse(targetURL)
	if err != nil {
		return fmt.Errorf("invalid target address: %v", err)
	}

	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   0, // No dial timeout
			KeepAlive: 0, // Let OS/user app manage keepalive
		}).DialContext,
		MaxIdleConns:        0, // Unlimited
		MaxIdleConnsPerHost: 0, // Unlimited
		IdleConnTimeout:     0, // No idle timeout
		DisableKeepAlives:   false,
		// Do not set ResponseHeaderTimeout, TLSHandshakeTimeout, etc.
	}

	// Configure transport for Unix domain sockets
	if strings.HasPrefix(p.targetAddr, "unix:") {
		socketPath := strings.TrimPrefix(p.targetAddr, "unix:")
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return net.Dial("unix", socketPath)
		}
	}

	p.proxy = &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
		},
		Transport: transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("Proxy error: %v", err)
			http.Error(w, "Proxy error", http.StatusBadGateway)
		},
	}

	return nil
}

// ServeHTTP handles HTTP requests, proxying them to the target if available
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !p.status.IsRunning() {
		http.Error(w, "Upstream service is not running", http.StatusServiceUnavailable)
		return
	}

	p.proxy.ServeHTTP(w, r)
}
