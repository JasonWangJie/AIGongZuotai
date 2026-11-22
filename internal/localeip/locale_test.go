package localeip

import (
	"bytes"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestLocaleDirectIP(t *testing.T) {
	for _, tc := range []struct{ addr, want string }{
		{"114.114.114.114", "zh-CN"}, {"203.198.7.66", "zh-TW"},
		{"168.95.1.1", "zh-TW"}, {"202.175.3.8", "zh-TW"}, {"133.242.0.1", "ja"},
		{"2400:3200::1", "zh-CN"}, {"2001:288::1", "zh-TW"}, {"2001:240::1", "ja"},
		{"::ffff:133.242.0.1", "ja"}, {"8.8.8.8", "en"}, {"2001:4860:4860::8888", "en"},
		{"127.0.0.1", "en"}, {"10.1.2.3", "en"}, {"fd00::1", "en"}, {"::1", "en"},
	} {
		req := httptest.NewRequest("GET", "/api/locale", nil)
		req.RemoteAddr = netip.AddrPortFrom(netip.MustParseAddr(tc.addr), 39400).String()
		req.Header.Set("X-Forwarded-For", "133.242.0.1")
		req.Header.Set("CF-IPCountry", "JP")
		if got := (Resolver{}).Language(req); got != tc.want {
			t.Errorf("language for %s = %s; want %s", tc.addr, got, tc.want)
		}
	}
}

func TestLocaleTrustedProxies(t *testing.T) {
	resolver, err := New(" 127.0.0.1/32,fd05::/64 ")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		chain []string
		want  string
	}{
		{[]string{"133.242.0.1"}, "ja"},
		{[]string{"133.242.0.1, 114.114.114.114"}, "zh-CN"},
		{[]string{"133.242.0.1, 2001:288::1", "fd05::a"}, "zh-TW"},
		{[]string{"114.114.114.114, 8.8.8.8"}, "en"},
		{[]string{"114.114.114.114, malformed"}, "en"},
		{[]string{"fd05::a"}, "en"},
		{[]string{strings.Repeat("fd05::a,", 33) + "114.114.114.114"}, "en"},
		{nil, "en"},
	} {
		r := httptest.NewRequest("GET", "/api/locale", nil)
		r.RemoteAddr = "127.0.0.1:58211"
		for _, value := range tc.chain {
			r.Header.Add("X-Forwarded-For", value)
		}
		if got := resolver.Language(r); got != tc.want {
			t.Errorf("language for %v = %s; want %s", tc.chain, got, tc.want)
		}
	}
	for _, invalid := range []string{"0.0.0.0/0", "::/0", "any", "127.0.0.1/32,", "::ffff:127.0.0.1/128"} {
		if _, err := New(invalid); err == nil {
			t.Errorf("accepted invalid proxy configuration %q", invalid)
		}
	}
}

func TestLocaleEmbeddedRanges(t *testing.T) {
	for width, records := range map[int][]byte{4: ipv4, 16: ipv6} {
		stride := width*2 + 1
		if len(records) == 0 || len(records)%stride != 0 {
			t.Fatal("truncated embedded table")
		}
		var previousEnd []byte
		for i := 0; i < len(records); i += stride {
			start, end := records[i:i+width], records[i+width:i+2*width]
			code := records[i+width*2]
			if bytes.Compare(start, end) > 0 || bytes.Compare(previousEnd, start) >= 0 || code < 1 || code > 3 {
				t.Fatalf("invalid range at byte %d", i)
			}
			previousEnd = end
		}
	}
}
