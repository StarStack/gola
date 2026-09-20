package gola

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// SetTrustedProxies replaces the trusted proxy list before serving begins.
// Entries must be IP addresses or CIDRs; nil or an empty list trusts no proxies.
// A parsing failure leaves the previous configuration intact.
func (e *Engine) SetTrustedProxies(cidrs []string) error {
	e.assertMutable()
	prefixes := make([]netip.Prefix, 0, len(cidrs))
	for _, entry := range cidrs {
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			addr, addrErr := netip.ParseAddr(entry)
			if addrErr != nil || addr.Zone() != "" {
				return fmt.Errorf("gola: invalid trusted proxy %q", entry)
			}
			addr = addr.Unmap()
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		} else if prefix.Addr().Is4In6() {
			if prefix.Bits() < 96 {
				return fmt.Errorf("gola: ambiguous IPv4-mapped proxy prefix %q", entry)
			}
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	e.trustedProxies = prefixes
	return nil
}

// ClientIP returns an IP literal, or "" when RemoteAddr is not an IP address.
// Forwarding headers are considered only from explicitly trusted direct peers.
// All X-Forwarded-For elements must be valid IP literals; the first untrusted
// address from the right is selected. An invalid supplied header fails closed.
func (c *Context) ClientIP() string {
	peer, ok := parsePeerIP(c.Request.RemoteAddr)
	if !ok {
		return ""
	}
	if !c.engine.isTrustedProxy(peer) {
		return peer.String()
	}
	if values, present := c.Request.Header["X-Forwarded-For"]; present {
		chain, valid := parseForwardedFor(values)
		if !valid {
			return peer.String()
		}
		for i := len(chain) - 1; i >= 0; i-- {
			if !c.engine.isTrustedProxy(chain[i]) || i == 0 {
				return chain[i].String()
			}
		}
	}
	if c.engine.trustRealIP {
		if values, present := c.Request.Header["X-Real-Ip"]; present {
			if len(values) != 1 {
				return peer.String()
			}
			addr, err := netip.ParseAddr(strings.TrimSpace(values[0]))
			if err == nil && addr.Zone() == "" {
				return addr.Unmap().String()
			}
		}
	}
	return peer.String()
}

func (e *Engine) isTrustedProxy(addr netip.Addr) bool {
	for _, prefix := range e.trustedProxies {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func parsePeerIP(remote string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.WithZone("").Unmap(), true
}

func parseForwardedFor(values []string) ([]netip.Addr, bool) {
	// Bound work even for custom http.Handler callers without Server header limits.
	const maxBytes, maxHops = 8192, 128
	bytes := 0
	chain := make([]netip.Addr, 0, min(len(values), maxHops))
	for _, value := range values {
		if len(value) > maxBytes-bytes {
			return nil, false
		}
		bytes += len(value)
		for part := range strings.SplitSeq(value, ",") {
			if len(chain) == maxHops {
				return nil, false
			}
			addr, err := netip.ParseAddr(strings.TrimSpace(part))
			if err != nil || addr.Zone() != "" {
				return nil, false
			}
			chain = append(chain, addr.Unmap())
		}
	}
	return chain, len(chain) > 0
}
