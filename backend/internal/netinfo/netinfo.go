package netinfo

import (
	"context"
	"net"
	"strconv"
	"strings"
	"time"
)

type IPInfo struct {
	IP     string `json:"ip"`
	ASN    string `json:"asn,omitempty"`
	ASName string `json:"as_name,omitempty"`
}

const lookupTimeout = 2 * time.Second

func ResolveHost(ctx context.Context, host string, limit int) []IPInfo {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" || limit <= 0 {
		return []IPInfo{}
	}
	lookupCtx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	records, err := net.DefaultResolver.LookupIPAddr(lookupCtx, host)
	if err != nil {
		return []IPInfo{}
	}
	out := make([]IPInfo, 0, min(len(records), limit))
	seen := map[string]bool{}
	for _, record := range records {
		ip := record.IP.String()
		if ip == "" || seen[ip] {
			continue
		}
		seen[ip] = true
		out = append(out, EnrichIP(ctx, ip))
		if len(out) >= limit {
			break
		}
	}
	return out
}

func EnrichIP(ctx context.Context, raw string) IPInfo {
	ip := net.ParseIP(strings.TrimSpace(strings.Trim(raw, "[]")))
	if ip == nil {
		return IPInfo{}
	}
	info := IPInfo{IP: ip.String()}
	if isPrivateIP(ip) {
		return info
	}
	asn, name := lookupASN(ctx, ip)
	info.ASN = asn
	info.ASName = name
	return info
}

func lookupASN(ctx context.Context, ip net.IP) (string, string) {
	query := cymruOriginQuery(ip)
	if query == "" {
		return "", ""
	}
	lookupCtx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	txts, err := net.DefaultResolver.LookupTXT(lookupCtx, query)
	if err != nil {
		return "", ""
	}
	asn := ""
	for _, txt := range txts {
		parts := strings.Split(txt, "|")
		if len(parts) > 0 {
			asn = strings.TrimSpace(parts[0])
			break
		}
	}
	if asn == "" {
		return "", ""
	}
	return asn, lookupASName(ctx, asn)
}

func lookupASName(ctx context.Context, asn string) string {
	lookupCtx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	txts, err := net.DefaultResolver.LookupTXT(lookupCtx, "AS"+asn+".asn.cymru.com")
	if err != nil {
		return ""
	}
	for _, txt := range txts {
		parts := strings.Split(txt, "|")
		if len(parts) >= 5 {
			return strings.TrimSpace(parts[4])
		}
	}
	return ""
}

func cymruOriginQuery(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return strings.Join([]string{
			itoaByte(v4[3]),
			itoaByte(v4[2]),
			itoaByte(v4[1]),
			itoaByte(v4[0]),
			"origin.asn.cymru.com",
		}, ".")
	}
	v6 := ip.To16()
	if v6 == nil {
		return ""
	}
	nibbles := make([]string, 0, 32)
	for i := len(v6) - 1; i >= 0; i-- {
		nibbles = append(nibbles, lowerHex(v6[i]&0x0f), lowerHex(v6[i]>>4))
	}
	return strings.Join(append(nibbles, "origin6.asn.cymru.com"), ".")
}

func itoaByte(value byte) string {
	return strconv.Itoa(int(value))
}

func lowerHex(value byte) string {
	const digits = "0123456789abcdef"
	return string(digits[value&0x0f])
}

func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() || ip.IsUnspecified()
}
