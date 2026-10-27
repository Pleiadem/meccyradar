package auth

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var stableVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func validStableVersion(value string) bool  { return stableVersionPattern.MatchString(value) }
func ValidReleaseVersion(value string) bool { return validStableVersion(value) }
func ValidDownloadURL(value string) bool    { return validDownloadURL(value) }

func CompareVersions(left, right string) int {
	l, r := stableVersionPattern.FindStringSubmatch(left), stableVersionPattern.FindStringSubmatch(right)
	if l == nil || r == nil {
		return 0
	}
	for i := 1; i < 4; i++ {
		if len(l[i]) < len(r[i]) {
			return -1
		}
		if len(l[i]) > len(r[i]) {
			return 1
		}
		if l[i] < r[i] {
			return -1
		}
		if l[i] > r[i] {
			return 1
		}
	}
	return 0
}

func validDownloadURL(raw string) bool {
	for _, r := range raw {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || (strings.ToLower(u.Scheme) != "http" && strings.ToLower(u.Scheme) != "https") || u.Hostname() == "" || u.User != nil {
		return false
	}
	if port := u.Port(); port != "" {
		for _, digit := range port {
			if digit < '0' || digit > '9' {
				return false
			}
		}
		parsedPort, err := strconv.Atoi(port)
		if err != nil || parsedPort < 1 || parsedPort > 65535 {
			return false
		}
	}
	return true
}
