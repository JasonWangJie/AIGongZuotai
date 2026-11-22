// Package localeip maps a visitor's network address to the initial UI language.
package localeip

import (
	"bytes"
	_ "embed"
	"errors"
	"net/http"
	"net/netip"
	"sort"
	"strings"
)

// Each fixed-width record is an inclusive start address, an inclusive end
// address (network byte order), and one language byte. See refresh.py.
//
//go:embed ipv4.bin
var ipv4 []byte

//go:embed ipv6.bin
var ipv6 []byte

type Resolver struct {
	proxies []netip.Prefix
}

func New(trustedProxies string) (Resolver, error) {
	var resolver Resolver
	if strings.TrimSpace(trustedProxies) == "" {
		return resolver, nil
	}
	entries := strings.Split(trustedProxies, ",")
	if len(entries) > 64 {
		return Resolver{}, errors.New("LOCALE_TRUSTED_PROXIES allows at most 64 proxy networks")
	}
	for _, entry := range entries {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(entry))
		if err != nil || prefix.Bits() == 0 || prefix.Addr().Is4In6() {
			return Resolver{}, errors.New("LOCALE_TRUSTED_PROXIES requires explicit IPv4 or IPv6 proxy CIDRs")
		}
		resolver.proxies = append(resolver.proxies, prefix.Masked())
	}
	return resolver, nil
}

func (resolver Resolver) trusts(addr netip.Addr) bool {
	for _, proxy := range resolver.proxies {
		if proxy.Contains(addr) {
			return true
		}
	}
	return false
}

func (resolver Resolver) Language(r *http.Request) string {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return "en"
	}
	addr := peer.Addr().Unmap()
	if resolver.trusts(addr) {
		// Work back from the actual socket peer. A visitor can forge values
		// to the left of the first untrusted hop.
		fields := r.Header.Values("X-Forwarded-For")
		chain := strings.Split(strings.Join(fields, ","), ",")
		if len(chain) > 32 {
			return "en"
		}
		for i := len(chain) - 1; i >= 0 && resolver.trusts(addr); i-- {
			addr, err = netip.ParseAddr(strings.TrimSpace(chain[i]))
			if err != nil || addr.Zone() != "" {
				return "en"
			}
			addr = addr.Unmap()
		}
		if resolver.trusts(addr) {
			return "en"
		}
	}
	return language(addr)
}

func language(ip netip.Addr) string {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return "en"
	}
	address, records := ip.AsSlice(), ipv6
	if ip.Is4() {
		records = ipv4
	}
	width := len(address)
	stride := width*2 + 1
	count := len(records) / stride
	i := sort.Search(count, func(i int) bool {
		offset := i*stride + width
		return bytes.Compare(records[offset:offset+width], address) >= 0
	})
	if i == count {
		return "en"
	}
	offset := i * stride
	if bytes.Compare(address, records[offset:offset+width]) < 0 {
		return "en"
	}
	switch records[offset+width*2] {
	case 1:
		return "zh-CN"
	case 2:
		return "zh-TW"
	case 3:
		return "ja"
	default:
		return "en"
	}
}
