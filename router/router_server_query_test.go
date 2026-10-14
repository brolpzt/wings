package router

import "testing"

func TestResolvePublicQueryHost(t *testing.T) {
	cases := []struct {
		name          string
		queryParam    string
		alias         string
		allocationIP  string
		localHost     string
		want          string
	}{
		{
			name:         "prefers query param",
			queryParam:   "89.39.161.156",
			alias:        "1.2.3.4",
			allocationIP: "10.50.0.126",
			localHost:    "172.24.0.2",
			want:         "89.39.161.156",
		},
		{
			name:         "prefers alias over private alloc",
			alias:        "89.39.161.156",
			allocationIP: "10.50.0.126",
			localHost:    "172.24.0.2",
			want:         "89.39.161.156",
		},
		{
			name:         "skips private alias",
			alias:        "10.0.0.1",
			allocationIP: "89.39.161.172",
			localHost:    "172.24.0.2",
			want:         "89.39.161.172",
		},
		{
			name:         "falls back to allocation when public",
			allocationIP: "203.0.113.10",
			localHost:    "172.24.0.2",
			want:         "203.0.113.10",
		},
		{
			name:         "skips bridge local host",
			allocationIP: "10.50.0.126",
			localHost:    "172.24.0.2",
			want:         "10.50.0.126",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolvePublicQueryHost(tc.queryParam, tc.alias, tc.allocationIP, tc.localHost)
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestIsNonPublicQueryHost(t *testing.T) {
	if !isNonPublicQueryHost("10.50.0.126") {
		t.Fatal("expected private")
	}
	if !isNonPublicQueryHost("172.24.0.2") {
		t.Fatal("expected private")
	}
	if isNonPublicQueryHost("89.39.161.156") {
		t.Fatal("expected public")
	}
	if isNonPublicQueryHost("game.example.com") {
		t.Fatal("hostname should be allowed")
	}
}
