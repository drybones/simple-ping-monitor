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
