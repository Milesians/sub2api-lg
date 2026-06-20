package netinfo

import (
	"context"
	"net"
	"testing"
)

func TestCymruOriginQueryIPv4(t *testing.T) {
	got := cymruOriginQuery(net.ParseIP("8.8.8.8"))
	want := "8.8.8.8.origin.asn.cymru.com"
	if got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
}

func TestEnrichPrivateIPDoesNotLookupASN(t *testing.T) {
	info := EnrichIP(context.Background(), "10.0.0.1")
	if info.IP != "10.0.0.1" || info.ASN != "" || info.ASName != "" {
		t.Fatalf("private ip info = %#v", info)
	}
}
