package probe

import (
	"context"
	"encoding/hex"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type RouteInfo struct {
	Host            string        `json:"host"`
	CheckedAt       string        `json:"checked_at"`
	IPs             []RouteIPInfo `json:"ips,omitempty"`
	Hops            []RouteHop    `json:"hops,omitempty"`
	Server          *ServerResult `json:"server,omitempty"`
	Error           string        `json:"error,omitempty"`
	TracerouteError string        `json:"traceroute_error,omitempty"`
}

type RouteIPInfo struct {
	IP  string   `json:"ip"`
	ASN *ASNInfo `json:"asn,omitempty"`
}

type ASNInfo struct {
	ASN       string `json:"asn,omitempty"`
	Prefix    string `json:"prefix,omitempty"`
	CC        string `json:"cc,omitempty"`
	Registry  string `json:"registry,omitempty"`
	Allocated string `json:"allocated,omitempty"`
	Name      string `json:"name,omitempty"`
}

type RouteHop struct {
	Index int       `json:"index"`
	IP    string    `json:"ip,omitempty"`
	RTTMS []float64 `json:"rtt_ms,omitempty"`
	ASN   *ASNInfo  `json:"asn,omitempty"`
	Raw   string    `json:"raw,omitempty"`
}

func RouteDiagnostics(ctx context.Context, host string) RouteInfo {
	target := cleanRouteHost(host)
	info := RouteInfo{
		Host:      target,
		CheckedAt: time.Now().Format(time.RFC3339),
	}
	if target == "" {
		info.Error = "empty_host"
		return info
	}

	ips, err := net.DefaultResolver.LookupIPAddr(ctx, target)
	if err != nil {
		info.Error = "dns_lookup_failed"
	} else {
		seen := map[string]bool{}
		for _, item := range ips {
			ip := item.IP
			if ip == nil {
				continue
			}
			key := ip.String()
			if seen[key] {
				continue
			}
			seen[key] = true
			info.IPs = append(info.IPs, RouteIPInfo{
				IP:  key,
				ASN: lookupASN(ctx, ip),
			})
			if len(info.IPs) >= 4 {
				break
			}
		}
	}

	info.Hops, info.TracerouteError = traceroute(ctx, target)
	return info
}

func cleanRouteHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return strings.Trim(host, "[]")
	}
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		return strings.Trim(parsedHost, "[]")
	}
	return strings.Trim(host, "[]")
}

func lookupASN(ctx context.Context, ip net.IP) *ASNInfo {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() {
		return nil
	}
	query := cymruOriginQuery(ip)
	if query == "" {
		return nil
	}
	txts, err := net.DefaultResolver.LookupTXT(ctx, query)
	if err != nil || len(txts) == 0 {
		return nil
	}
	parts := splitCymruTXT(txts[0])
	if len(parts) == 0 || strings.EqualFold(parts[0], "na") {
		return nil
	}
	asn := strings.Fields(parts[0])
	if len(asn) == 0 {
		return nil
	}
	info := &ASNInfo{ASN: asn[0]}
	if len(parts) > 1 {
		info.Prefix = parts[1]
	}
	if len(parts) > 2 {
		info.CC = parts[2]
	}
	if len(parts) > 3 {
		info.Registry = parts[3]
	}
	if len(parts) > 4 {
		info.Allocated = parts[4]
	}
	info.Name = lookupASName(ctx, info.ASN)
	return info
}

func lookupASName(ctx context.Context, asn string) string {
	if asn == "" {
		return ""
	}
	txts, err := net.DefaultResolver.LookupTXT(ctx, "AS"+asn+".asn.cymru.com")
	if err != nil || len(txts) == 0 {
		return ""
	}
	parts := splitCymruTXT(txts[0])
	if len(parts) < 5 {
		return ""
	}
	return parts[4]
}

func splitCymruTXT(raw string) []string {
	items := strings.Split(raw, "|")
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, strings.TrimSpace(item))
	}
	return out
}

func cymruOriginQuery(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return strconv.Itoa(int(v4[3])) + "." +
			strconv.Itoa(int(v4[2])) + "." +
			strconv.Itoa(int(v4[1])) + "." +
			strconv.Itoa(int(v4[0])) + ".origin.asn.cymru.com"
	}
	v6 := ip.To16()
	if v6 == nil {
		return ""
	}
	encoded := hex.EncodeToString(v6)
	labels := make([]string, 0, len(encoded))
	for i := len(encoded) - 1; i >= 0; i-- {
		labels = append(labels, encoded[i:i+1])
	}
	return strings.Join(labels, ".") + ".origin6.asn.cymru.com"
}

func traceroute(ctx context.Context, host string) ([]RouteHop, string) {
	path, err := exec.LookPath("traceroute")
	if err != nil {
		return nil, "traceroute_not_available"
	}
	traceCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	output, err := exec.CommandContext(traceCtx, path, "-n", "-m", "12", "-w", "1", "-q", "2", host).CombinedOutput()
	hops := parseTracerouteOutput(ctx, string(output))
	if traceCtx.Err() != nil {
		return hops, "traceroute_timeout"
	}
	if err != nil && len(hops) == 0 {
		return nil, "traceroute_failed"
	}
	if err != nil {
		return hops, "traceroute_partial"
	}
	return hops, ""
}

func parseTracerouteOutput(ctx context.Context, output string) []RouteHop {
	lines := strings.Split(output, "\n")
	hops := make([]RouteHop, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		fields := strings.Fields(trimmed)
		if len(fields) == 0 {
			continue
		}
		index, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		hop := RouteHop{Index: index, Raw: trimmed}
		for i := 1; i < len(fields); i++ {
			candidate := strings.Trim(fields[i], "[](),")
			if ip := net.ParseIP(candidate); ip != nil {
				hop.IP = ip.String()
				continue
			}
			if fields[i] == "ms" && i > 1 {
				if rtt, err := strconv.ParseFloat(fields[i-1], 64); err == nil {
					hop.RTTMS = append(hop.RTTMS, rtt)
				}
				continue
			}
			if strings.HasSuffix(fields[i], "ms") {
				rawRTT := strings.TrimSuffix(fields[i], "ms")
				if rtt, err := strconv.ParseFloat(rawRTT, 64); err == nil {
					hop.RTTMS = append(hop.RTTMS, rtt)
				}
			}
		}
		if hop.IP != "" {
			hop.ASN = lookupASN(ctx, net.ParseIP(hop.IP))
		}
		if hop.IP != "" || strings.Contains(trimmed, "*") {
			hops = append(hops, hop)
		}
	}
	return hops
}
