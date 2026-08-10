package browserpolicy

import (
	"net"
	"strings"
	"testing"
)

func TestNormalizePublicOrigin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "https host", raw: "https://example.com", want: "https://example.com"},
		{name: "http host", raw: "http://example.com", want: "http://example.com"},
		{name: "default https port dropped", raw: "https://example.com:443", want: "https://example.com"},
		{name: "default http port dropped", raw: "http://example.com:80", want: "http://example.com"},
		{name: "non-default port kept", raw: "https://example.com:8443", want: "https://example.com:8443"},
		{name: "scheme and host normalized", raw: "HTTP://Example.COM", want: "http://example.com"},
		{name: "trailing dot trimmed", raw: "https://example.com.", want: "https://example.com"},
		{name: "public ipv4", raw: "http://93.184.216.34", want: "http://93.184.216.34"},
		{name: "public ipv4 with port", raw: "http://93.184.216.34:8080", want: "http://93.184.216.34:8080"},
		{name: "public ipv6", raw: "https://[2606:4700::6810:84e5]", want: "https://[2606:4700::6810:84e5]"},

		{name: "localhost rejected", raw: "http://localhost", wantErr: true},
		{name: "localhost subdomain rejected", raw: "http://app.localhost", wantErr: true},
		{name: "metadata host rejected", raw: "http://metadata.google.internal", wantErr: true},
		{name: "loopback ip rejected", raw: "http://127.0.0.1", wantErr: true},
		{name: "private ip rejected", raw: "http://10.0.0.1", wantErr: true},
		{name: "rfc1918 c rejected", raw: "http://192.168.1.1", wantErr: true},
		{name: "link local rejected", raw: "http://169.254.169.254", wantErr: true},
		{name: "cgNat rejected", raw: "http://100.64.0.1", wantErr: true},
		{name: "benchmarking rejected", raw: "http://198.18.0.1", wantErr: true},
		{name: "reserved rejected", raw: "http://240.0.0.1", wantErr: true},
		{name: "test-net rejected", raw: "http://192.0.2.1", wantErr: true},
		{name: "decimal loopback rejected", raw: "http://2130706433", wantErr: true},
		{name: "octal loopback rejected", raw: "http://0177.0.0.1", wantErr: true},
		{name: "ipv6 loopback rejected", raw: "https://[::1]", wantErr: true},
		{name: "ipv6 ula rejected", raw: "https://[fc00::1]", wantErr: true},
		{name: "ipv6 link-local rejected", raw: "https://[fe80::1]", wantErr: true},
		{name: "ipv6 doc prefix rejected", raw: "https://[2001:db8::1]", wantErr: true},

		{name: "path rejected", raw: "https://example.com/path", wantErr: true},
		{name: "query rejected", raw: "https://example.com?q=1", wantErr: true},
		{name: "fragment rejected", raw: "https://example.com#frag", wantErr: true},
		{name: "user info rejected", raw: "https://user@example.com", wantErr: true},
		{name: "empty rejected", raw: "", wantErr: true},
		{name: "whitespace rejected", raw: " https://example.com", wantErr: true},
		{name: "wildcard rejected", raw: "https://*.example.com", wantErr: true},
		{name: "single label rejected", raw: "https://intranet", wantErr: true},
		{name: "label leading hyphen rejected", raw: "https://-bad.example.com", wantErr: true},
		{name: "label trailing hyphen rejected", raw: "https://bad-.example.com", wantErr: true},
		{name: "double dot rejected", raw: "https://exa..mple.com", wantErr: true},
		{name: "invalid scheme rejected", raw: "ftp://example.com", wantErr: true},
		{name: "zero port rejected", raw: "https://example.com:0", wantErr: true},
		{name: "overflow port rejected", raw: "https://example.com:99999", wantErr: true},
		{name: "empty port rejected", raw: "https://example.com:", wantErr: true},
		{name: "too long rejected", raw: "https://example.com/" + strings.Repeat("x", 3000), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizePublicOrigin(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NormalizePublicOrigin(%q) = %q, want error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizePublicOrigin(%q) error = %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizePublicOrigin(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestNormalizeHTTPOriginSkipsAddressScopePolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "localhost kept", raw: "http://localhost:8080", want: "http://localhost:8080"},
		{name: "loopback kept", raw: "http://127.0.0.1:8080", want: "http://127.0.0.1:8080"},
		{name: "private ip kept", raw: "http://192.168.1.1", want: "http://192.168.1.1"},
		{name: "public host kept", raw: "https://example.com", want: "https://example.com"},
		{name: "ambiguous numeric ipv4 rejected", raw: "http://2130706433", wantErr: true},
		{name: "path still rejected", raw: "https://example.com/path", wantErr: true},
		{name: "invalid scheme still rejected", raw: "ftp://example.com", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeHTTPOrigin(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NormalizeHTTPOrigin(%q) = %q, want error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeHTTPOrigin(%q) error = %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeHTTPOrigin(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestIsPublicIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ip   net.IP
		want bool
	}{
		{name: "nil", ip: nil, want: false},
		{name: "public ipv4", ip: net.ParseIP("8.8.8.8"), want: true},
		{name: "public ipv4 alt", ip: net.ParseIP("93.184.216.34"), want: true},
		{name: "loopback", ip: net.ParseIP("127.0.0.1"), want: false},
		{name: "private a", ip: net.ParseIP("10.1.2.3"), want: false},
		{name: "private b", ip: net.ParseIP("172.16.0.1"), want: false},
		{name: "private c", ip: net.ParseIP("192.168.1.1"), want: false},
		{name: "link local", ip: net.ParseIP("169.254.169.254"), want: false},
		{name: "cgNat", ip: net.ParseIP("100.64.0.1"), want: false},
		{name: "unspecified", ip: net.ParseIP("0.0.0.0"), want: false},
		{name: "broadcast", ip: net.ParseIP("255.255.255.255"), want: false},
		{name: "multicast", ip: net.ParseIP("224.0.0.1"), want: false},
		{name: "public ipv6", ip: net.ParseIP("2606:4700::6810:84e5"), want: true},
		{name: "ipv6 loopback", ip: net.ParseIP("::1"), want: false},
		{name: "ipv6 ula", ip: net.ParseIP("fc00::1"), want: false},
		{name: "ipv6 link local", ip: net.ParseIP("fe80::1"), want: false},
		{name: "ipv6 doc prefix", ip: net.ParseIP("2001:db8::1"), want: false},
		{name: "ipv4 mapped public", ip: net.ParseIP("::ffff:8.8.8.8"), want: true},
		{name: "ipv4 mapped loopback", ip: net.ParseIP("::ffff:127.0.0.1"), want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsPublicIP(tc.ip); got != tc.want {
				t.Fatalf("IsPublicIP(%v) = %v, want %v", tc.ip, got, tc.want)
			}
		})
	}
}
