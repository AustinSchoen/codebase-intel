// Package discovery announces and locates a codebase-intel server on the
// local network via mDNS / Zeroconf. The server publishes itself as
// `_codebase-intel._tcp.local` with the bearer token in the TXT record;
// indexer hosts can find it by service name without any manual URL or
// token entry.
//
// This is LAN-only by design — mDNS doesn't cross subnets. Networks where
// mDNS is blocked (corporate Wi-Fi, hotel Wi-Fi, some VPNs) won't find the
// server; callers should fall back to manual configuration.
//
// Security: the bearer token in the TXT record is broadcast on the LAN,
// so anyone on the network can read it. This matches the trust model of
// the LAN itself (Wi-Fi password protects everything else on it). Operators
// who want tighter isolation should disable advertising via the server
// config and connect indexers manually.
package discovery

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"
)

// ServiceName is the mDNS service type for the codebase-intel MCP server.
const ServiceName = "_codebase-intel._tcp"

// AdvertiseOptions configures what the server announces over mDNS.
type AdvertiseOptions struct {
	// InstanceName uniquely identifies this server instance on the LAN.
	// Defaults to "codebase-intel-<hostname>".
	InstanceName string

	// Port the server's HTTP API is listening on.
	Port int

	// Token is the bearer token consumers should use to authenticate.
	// If empty, no token is published — discovery will surface the URL
	// but consumers must obtain the token out-of-band.
	Token string
}

// Advertiser is the handle returned by Advertise; call Shutdown to stop
// announcing.
type Advertiser interface {
	Shutdown()
}

// Advertise starts publishing the codebase-intel service on mDNS. The
// returned Advertiser must be Shutdown when the server stops to remove
// the entry from the LAN cache.
func Advertise(opts AdvertiseOptions) (Advertiser, error) {
	if opts.Port == 0 {
		return nil, fmt.Errorf("discovery: Port is required")
	}
	if opts.InstanceName == "" {
		host, err := os.Hostname()
		if err != nil {
			host = "unknown"
		}
		opts.InstanceName = "codebase-intel-" + sanitize(host)
	}

	txt := []string{"version=1"}
	if opts.Token != "" {
		txt = append(txt, "token="+opts.Token)
	}

	server, err := zeroconf.Register(
		opts.InstanceName,
		ServiceName,
		"local.",
		opts.Port,
		txt,
		nil, // all interfaces
	)
	if err != nil {
		return nil, fmt.Errorf("discovery: register: %w", err)
	}
	return server, nil
}

// Service is a single discovered codebase-intel server.
type Service struct {
	InstanceName string
	Host         string // resolved IP (prefers IPv4)
	Port         int
	Token        string // empty if the announcement omitted it
	URL          string // convenience: http://<host>:<port>
}

// Discover queries the LAN for codebase-intel server announcements and
// returns every responder seen before the context's deadline (or before
// timeout elapses, whichever is shorter). The slice is empty if no server
// responded — callers should fall back to manual configuration in that case.
func Discover(ctx context.Context, timeout time.Duration) ([]Service, error) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return nil, fmt.Errorf("discovery: new resolver: %w", err)
	}

	entries := make(chan *zeroconf.ServiceEntry, 8)
	browseCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := resolver.Browse(browseCtx, ServiceName, "local.", entries); err != nil {
		return nil, fmt.Errorf("discovery: browse: %w", err)
	}

	var services []Service
	for entry := range entries {
		svc := serviceFromEntry(entry)
		if svc.Host == "" || svc.Port == 0 {
			// Incomplete announcement — skip rather than surface a half record.
			continue
		}
		services = append(services, svc)
	}
	return services, nil
}

func serviceFromEntry(e *zeroconf.ServiceEntry) Service {
	svc := Service{
		InstanceName: e.Instance,
		Port:         e.Port,
	}
	svc.Host = pickAddress(e.AddrIPv4, e.AddrIPv6)
	for _, kv := range e.Text {
		if k, v, ok := strings.Cut(kv, "="); ok && k == "token" {
			svc.Token = v
		}
	}
	if svc.Host != "" && svc.Port != 0 {
		svc.URL = fmt.Sprintf("http://%s:%d", svc.Host, svc.Port)
	}
	return svc
}

// pickAddress prefers IPv4 because most operators' MCP server configs use
// it; falls back to IPv6 only if no IPv4 address was announced.
func pickAddress(v4, v6 []net.IP) string {
	for _, ip := range v4 {
		if ip != nil && !ip.IsUnspecified() {
			return ip.String()
		}
	}
	for _, ip := range v6 {
		if ip != nil && !ip.IsUnspecified() {
			return ip.String()
		}
	}
	return ""
}

// sanitize strips characters from a hostname that would be awkward in an
// mDNS instance name. We replace whitespace and dots with hyphens; the
// underlying library handles the wire-format quoting.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '.' || r == ' ' || r == '\t':
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
