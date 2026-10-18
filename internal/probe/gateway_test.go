package probe

import (
	"strings"
	"testing"
)

func TestParseRouteGet(t *testing.T) {
	out := `   route to: default
destination: default
       mask: default
    gateway: 192.168.1.254
  interface: en0
      flags: <UP,GATEWAY,DONE,STATIC,PRCLONING,GLOBAL>
`
	gw, err := parseRouteGet(out)
	if err != nil || gw != "192.168.1.254" {
		t.Fatalf("got %q, %v", gw, err)
	}
	if _, err := parseRouteGet("route: writing to routing socket: not in table\n"); err == nil {
		t.Error("want an error with no default route")
	}
}

func TestParseProcNetRoute(t *testing.T) {
	table := `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
eth0	0000A8C0	00000000	0001	0	0	0	00FFFFFF	0	0	0
eth0	00000000	0101A8C0	0003	0	0	0	00000000	0	0	0
`
	gw, err := parseProcNetRoute(strings.NewReader(table))
	if err != nil || gw != "192.168.1.1" {
		t.Fatalf("got %q, %v", gw, err)
	}
}

func TestParseTraceroute(t *testing.T) {
	const header = "traceroute to 8.8.8.8 (8.8.8.8), 3 hops max, 40 byte packets\n"
	cases := []struct {
		name, gateway, out string
		want               []string
	}{
		{"double NAT", "192.168.10.1", ` 1  192.168.10.1  3.112 ms
 2  192.168.1.1  9.245 ms
 3  62.72.136.165  16.434 ms
`, []string{"192.168.1.1"}},
		{"single router", "192.168.1.1", ` 1  192.168.1.1  2.482 ms
 2  62.72.136.165  16.434 ms
 3  *
`, nil},
		{"ISP uses 10.x", "192.168.1.1", ` 1  192.168.1.1  2.4 ms
 2  10.20.0.1  12.1 ms
`, nil},
		{"no answer", "192.168.10.1", ` 1  192.168.10.1  3.1 ms
 2  *
 3  192.168.1.1  9.2 ms
`, nil},
		{"first hop isn't the gateway", "192.168.1.1", ` 1  192.168.50.1  3.1 ms
 2  192.168.1.1  9.2 ms
`, nil},
	}
	for _, c := range cases {
		got := parseTraceroute(header+c.out, c.gateway)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
