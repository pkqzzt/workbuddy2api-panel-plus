package server

import (
	"net"
	"testing"
)

// TestValidateFetchTargetRejectsInternal 覆盖 SSRF 的主要绕过面。
//
// 这个端点会让网关代取任意 URL，因此回环、私网、链路本地（含云元数据
// 169.254.169.254）、CGNAT、IPv6 ULA 以及非 http(s) 协议都必须被拒。
func TestValidateFetchTargetRejectsInternal(t *testing.T) {
	blocked := []string{
		"http://127.0.0.1/",
		"http://127.1.2.3:8080/x",
		"http://localhost/",
		"http://10.0.0.5/",
		"http://172.16.31.9/",
		"http://192.168.1.1/",
		"http://169.254.169.254/latest/meta-data/",
		"http://100.64.0.1/",
		"http://[::1]/",
		"http://[fc00::1]/",
		"file:///etc/passwd",
		"ftp://example.com/",
		"gopher://127.0.0.1:6379/",
		"",
		"http://",
	}
	for _, raw := range blocked {
		if _, err := validateFetchTarget(raw); err == nil {
			t.Errorf("validateFetchTarget(%q) 应被拒绝，却通过了", raw)
		}
	}
}

// TestValidateFetchTargetAllowsPublic 公网地址必须放行（否则工具不可用）。
func TestValidateFetchTargetAllowsPublic(t *testing.T) {
	for _, raw := range []string{"https://example.com/a?b=1", "http://1.1.1.1/"} {
		if _, err := validateFetchTarget(raw); err != nil {
			t.Errorf("validateFetchTarget(%q) 应通过，却报错: %v", raw, err)
		}
	}
}

// TestIsPublicIP 逐类验证 IP 判定。
func TestIsPublicIP(t *testing.T) {
	cases := map[string]bool{
		"8.8.8.8":         true,
		"1.1.1.1":         true,
		"127.0.0.1":       false,
		"10.1.2.3":        false,
		"172.16.0.1":      false,
		"192.168.0.1":     false,
		"169.254.169.254": false,
		"100.100.100.100": false,
		"0.0.0.0":         false,
		"::1":             false,
		"fe80::1":         false,
		"fc00::1":         false,
	}
	for ipStr, want := range cases {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			t.Fatalf("解析失败: %s", ipStr)
		}
		if got := isPublicIP(ip); got != want {
			t.Errorf("isPublicIP(%s) = %v, want %v", ipStr, got, want)
		}
	}
}
