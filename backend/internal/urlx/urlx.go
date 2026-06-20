package urlx

import (
	"errors"
	"net"
	"net/url"
	"path"
	"strings"
)

func Join(baseURL, pathValue string) (string, error) {
	if strings.TrimSpace(baseURL) == "" {
		return "", errors.New("base url is empty")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", errors.New("base url must include scheme and host")
	}
	p := strings.TrimSpace(pathValue)
	if p == "" {
		return canonicalURL(u), nil
	}
	p = "/" + strings.TrimLeft(p, "/")
	u.Path = path.Join(u.Path, p)
	u.RawQuery = ""
	u.Fragment = ""
	return canonicalURL(u), nil
}

func CanonicalBase(raw string) (*url.URL, string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, "", errors.New("missing scheme or host")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.RawQuery = ""
	u.Fragment = ""
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path == "" {
		u.Path = ""
	}
	return u, canonicalURL(u), nil
}

func IsPrivateHost(host string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	if h == "localhost" || h == "metadata.google.internal" {
		return true
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	return isPrivateIP(ip)
}

func Origin(u *url.URL) string {
	out := &url.URL{Scheme: u.Scheme, Host: u.Host}
	return out.String()
}

func canonicalURL(u *url.URL) string {
	copy := *u
	if copy.Path == "/" {
		copy.Path = ""
	}
	return copy.String()
}

func isPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() || ip.IsUnspecified() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		return ip4[0] == 0 || (ip4[0] == 169 && ip4[1] == 254)
	}
	return false
}
