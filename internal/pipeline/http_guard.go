package pipeline

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"
)

// errBlockedAddress is returned when an http.request would connect to an
// address SupaCraft refuses to reach.
var errBlockedAddress = errors.New("destination address is not allowed")

// alwaysBlocked rejects addresses no pipeline has a reason to call:
// link-local (including the 169.254.169.254 cloud metadata service),
// unspecified and multicast addresses.
func alwaysBlocked(ip netip.Addr) bool {
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast()
}

// internalAddress additionally covers loopback and private networks. These
// are blocked when the URL's host comes from a template, so request data
// cannot point the server at its own internal network.
func internalAddress(ip netip.Addr) bool {
	return alwaysBlocked(ip) || ip.IsLoopback() || ip.IsPrivate() ||
		cgnat.Contains(ip)
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// guardedClient checks every connection — including redirects and each DNS
// answer — against blocked at dial time, so DNS rebinding can't bypass it.
func guardedClient(blocked func(netip.Addr) bool, useEnvProxy bool) *http.Client {
	dialer := &net.Dialer{
		Timeout: 30 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil {
				return err
			}
			if blocked(ip.Unmap()) {
				return fmt.Errorf("%w: %s", errBlockedAddress, ip)
			}
			return nil
		},
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if !useEnvProxy {
		// Through a proxy the check would only see the proxy's address.
		transport.Proxy = nil
	}
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, addr)
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("http.request: too many redirects (max 5)")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("http.request: redirect to %q is not allowed", req.URL.Scheme)
			}
			return nil
		},
	}
}

var (
	// httpClient serves URLs whose host is fixed in supacraft.json.
	httpClient = guardedClient(alwaysBlocked, true)
	// strictHTTPClient serves URLs whose host is built from a template.
	strictHTTPClient = guardedClient(internalAddress, false)
)

// templatedHost reports whether the scheme or host of a configured URL
// contains a {{...}} reference.
func templatedHost(rawURL string) bool {
	rest := rawURL
	if i := strings.Index(rest, "://"); i >= 0 {
		if strings.Contains(rest[:i], "{{") {
			return true
		}
		rest = rest[i+3:]
	} else {
		// No literal scheme: the whole URL, host included, is templated.
		return strings.Contains(rest, "{{")
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	return strings.Contains(rest, "{{")
}
