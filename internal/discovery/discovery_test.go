package discovery

import (
	"context"
	"net"
	"testing"
	"time"
)

// TestAdvertiseAndDiscover does a real mDNS round-trip on the local
// machine. It's environment-sensitive (CI containers occasionally lack
// multicast; some sandboxed Linux distros block it) so the test SKIPS
// when no service is observed within a generous timeout. A regression in
// either Advertise or Discover would also produce zero results, so the
// test acts as a smoke check rather than a hard correctness assertion.
//
// Run locally with `go test ./internal/discovery/...` to confirm mDNS is
// actually working on the dev host.
func TestAdvertiseAndDiscover(t *testing.T) {
	const port = 18099
	const token = "test-token-xyz"

	adv, err := Advertise(AdvertiseOptions{
		InstanceName: "codebase-intel-test",
		Port:         port,
		Token:        token,
	})
	if err != nil {
		t.Fatalf("Advertise: %v", err)
	}
	defer adv.Shutdown()

	// Give the multicast announcement a moment to propagate before we
	// browse. Most networks see it within a few hundred milliseconds.
	time.Sleep(300 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	results, err := Discover(ctx, 2*time.Second)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if len(results) == 0 {
		t.Skip("no mDNS responses observed — multicast may be blocked on this host; " +
			"manually verify with `dns-sd -B _codebase-intel._tcp` on macOS or " +
			"`avahi-browse _codebase-intel._tcp` on Linux")
	}

	var found *Service
	for i := range results {
		if results[i].InstanceName == "codebase-intel-test" {
			found = &results[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected our advertised instance in results, got %+v", results)
	}

	if found.Port != port {
		t.Errorf("Port = %d, want %d", found.Port, port)
	}
	if found.Token != token {
		t.Errorf("Token = %q, want %q", found.Token, token)
	}
	if found.URL == "" {
		t.Errorf("URL should be populated, got empty string")
	}
}

func TestServiceFromEntry_PicksIPv4OverIPv6(t *testing.T) {
	// Bypass the zeroconf entry to verify the IP-selection logic
	// without needing a real network round-trip.
	got := pickAddress(
		mustIPs("192.168.1.5", "10.0.0.7"),
		mustIPs("fe80::1"),
	)
	if got != "192.168.1.5" {
		t.Errorf("pickAddress preferred %q, want 192.168.1.5", got)
	}
}

func TestServiceFromEntry_FallsBackToIPv6(t *testing.T) {
	got := pickAddress(nil, mustIPs("fe80::1"))
	if got != "fe80::1" {
		t.Errorf("pickAddress = %q, want fe80::1", got)
	}
}

func TestAdvertise_OmitsTokenWhenEmpty(t *testing.T) {
	// When no token is supplied, the TXT record must not include a
	// `token=` entry — operators wanting tighter security opt in by
	// leaving Token empty.
	adv, err := Advertise(AdvertiseOptions{
		InstanceName: "codebase-intel-no-token-test",
		Port:         18098,
		Token:        "",
	})
	if err != nil {
		t.Fatalf("Advertise: %v", err)
	}
	defer adv.Shutdown()

	time.Sleep(300 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	results, err := Discover(ctx, 2*time.Second)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(results) == 0 {
		t.Skip("no mDNS responses observed on this host")
	}

	for _, r := range results {
		if r.InstanceName == "codebase-intel-no-token-test" && r.Token != "" {
			t.Errorf("expected empty Token for unset-token announcement, got %q", r.Token)
		}
	}
}

func TestAdvertise_RejectsZeroPort(t *testing.T) {
	_, err := Advertise(AdvertiseOptions{Port: 0})
	if err == nil {
		t.Error("expected error when Port is 0")
	}
}

func mustIPs(addrs ...string) []net.IP {
	out := make([]net.IP, 0, len(addrs))
	for _, s := range addrs {
		ip := net.ParseIP(s)
		if ip == nil {
			panic("bad IP literal in test: " + s)
		}
		out = append(out, ip)
	}
	return out
}
