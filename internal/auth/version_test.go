package auth

import "testing"

func TestStableVersionsCompareNumerically(t *testing.T) {
	if !ValidReleaseVersion("1.0.0") || ValidReleaseVersion("1.0") || ValidReleaseVersion("01.0.0") {
		t.Fatal("invalid stable version validation")
	}
	if CompareVersions("1.10.0", "1.9.0") <= 0 {
		t.Fatal("versions must compare numerically")
	}
	if CompareVersions("1.0.0", "1.0.0") != 0 {
		t.Fatal("equal versions should compare equal")
	}
}

func TestDownloadURLAcceptsHTTPAndHTTPSWithoutUserinfo(t *testing.T) {
	for _, raw := range []string{
		"https://downloads.example.test/release#install",
		"http://downloads.example.test:80/release?channel=stable#latest",
		"https://downloads.example.test:443/release#sha256",
		"https://downloads.example.test:8443/release#mirror",
		"http://127.0.0.1:8080/release#local",
	} {
		if !ValidDownloadURL(raw) {
			t.Fatalf("valid URL rejected: %s", raw)
		}
	}
	for _, raw := range []string{
		"javascript:alert(1)", "file:///etc/passwd", "https://user@example.com/a",
		"https://:password@example.com/a", "https://example.com:0/a", "https://example.com:65536/a",
		"https://example.com/a\nb", "https://example.com/a b",
	} {
		if ValidDownloadURL(raw) {
			t.Fatalf("unsafe or malformed URL accepted: %q", raw)
		}
	}
}
