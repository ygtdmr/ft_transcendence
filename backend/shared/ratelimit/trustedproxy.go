package ratelimit

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// maxForwardedEntries bounds how much of X-Forwarded-For is examined. A real
// chain of proxies is a handful of hops; more is an attempt to burn CPU or to
// hide behind padding.
const maxForwardedEntries = 20

// TrustedProxies lists the reverse proxies whose X-Forwarded-For header may be
// believed. Behind the platform's reverse proxy every connection's peer is the
// proxy, so a limiter keyed on the peer would throttle all users as one. The
// header is attacker-controlled everywhere except where a trusted proxy wrote
// it, so it is only read when the peer itself is trusted, and only the part
// the trusted proxies added is believed.
type TrustedProxies struct {
	prefixes []netip.Prefix
}

// ParseTrustedProxies parses CIDR ranges or single addresses ("10.0.0.0/8",
// "172.18.0.5", "fd00::/8"). An empty list trusts no proxy, which makes
// ClientIP ignore forwarding headers completely.
func ParseTrustedProxies(entries []string) (*TrustedProxies, error) {
	t := &TrustedProxies{}
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			addr, addrErr := netip.ParseAddr(entry)
			if addrErr != nil {
				return nil, errors.New("ratelimit: a trusted proxy must be an address or a CIDR range")
			}
			prefix = netip.PrefixFrom(addr.Unmap(), addr.Unmap().BitLen())
		}
		t.prefixes = append(t.prefixes, prefix.Masked())
	}
	return t, nil
}

func (t *TrustedProxies) trusts(addr netip.Addr) bool {
	if t == nil {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range t.prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// ClientIP returns the address to attribute r to.
//
//   - The peer (RemoteAddr) is the answer unless it is a trusted proxy.
//   - If the peer is trusted, X-Forwarded-For is read from the right: entries
//     added by trusted proxies are skipped and the first address that is not
//     trusted is the client. Anything to its left was supplied by the client and
//     is never believed.
//   - A malformed or oversized header, or one where every hop is trusted, falls
//     back to the peer rather than to a value the client chose.
func (t *TrustedProxies) ClientIP(r *http.Request) string {
	peerText, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peerText = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(peerText)
	if err != nil {
		return peerText
	}
	peer = peer.Unmap()
	if !t.trusts(peer) {
		return peer.String()
	}

	var hops []string
	for _, line := range r.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(line, ",") {
			hops = append(hops, strings.TrimSpace(part))
		}
	}
	if len(hops) == 0 || len(hops) > maxForwardedEntries {
		return peer.String()
	}
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(hops[i])
		if err != nil {
			return peer.String()
		}
		hop = hop.Unmap()
		if !t.trusts(hop) {
			return hop.String()
		}
	}
	return peer.String()
}

// KeyByClientIP keys by the client address resolved through the trusted
// proxies (see ClientIP), grouping IPv6 clients by /64 like KeyByRemoteAddr.
func KeyByClientIP(t *TrustedProxies) func(*http.Request) string {
	return func(r *http.Request) string {
		addr, err := netip.ParseAddr(t.ClientIP(r))
		if err != nil {
			return t.ClientIP(r)
		}
		if addr.Is6() {
			prefix, _ := addr.Prefix(64)
			return prefix.String()
		}
		return addr.String()
	}
}
