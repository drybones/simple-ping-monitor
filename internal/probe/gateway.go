package probe

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// DefaultGateway returns the IPv4 address of the default route's gateway,
// which on a typical home or church network is the router.
func DefaultGateway() (string, error) {
	switch runtime.GOOS {
	case "darwin", "freebsd", "openbsd", "netbsd":
		out, err := exec.Command("route", "-n", "get", "default").Output()
		if err != nil {
			return "", err
		}
		return parseRouteGet(string(out))
	case "linux":
		f, err := os.Open("/proc/net/route")
		if err != nil {
			return "", err
		}
		defer f.Close()
		return parseProcNetRoute(f)
	}
	return "", errors.New("gateway discovery not supported on " + runtime.GOOS)
}

// parseRouteGet reads the output of BSD/macOS `route -n get default`.
func parseRouteGet(out string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok && k == "gateway" {
			gw := strings.TrimSpace(v)
			if net.ParseIP(gw).To4() != nil {
				return gw, nil
			}
		}
	}
	return "", errors.New("no IPv4 default gateway found")
}

// parseProcNetRoute reads Linux /proc/net/route, whose addresses are
// little-endian hex.
func parseProcNetRoute(f io.Reader) (string, error) {
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 || fields[1] != "00000000" {
			continue
		}
		b, err := hex.DecodeString(fields[2])
		if err != nil || len(b) != 4 {
			continue
		}
		ip := make(net.IP, 4)
		binary.BigEndian.PutUint32(ip, binary.LittleEndian.Uint32(b))
		if !ip.IsUnspecified() {
			return ip.String(), nil
		}
	}
	return "", errors.New("no IPv4 default gateway found")
}

// UpstreamRouters finds routers behind the default gateway that are still
// inside the building: a Wi-Fi access point or extender running in router
// mode in front of the main router (double NAT) hides the main router, and
// whatever links the two (powerline adapters, say), from the gateway ping.
// It traces the route to host with the system traceroute, which needs no
// sudo on macOS, and returns the hops after the gateway, nearest first.
func UpstreamRouters(gateway, host string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "traceroute", "-n", "-q", "1", "-w", "1", "-m", "3", host).Output()
	if err != nil {
		return nil, err
	}
	return parseTraceroute(string(out), gateway), nil
}

// parseTraceroute reads `traceroute -n -q 1` output. The first hop must be
// the gateway; the hops after it count while they answer from 192.168.x.x.
// 10.x and 172.16–31.x are left out because ISPs number their own routers
// from those ranges, so such a hop may well be outside the building.
func parseTraceroute(out, gateway string) []string {
	var hops []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		n, err := strconv.Atoi(f[0])
		if err != nil {
			continue // the "traceroute to ..." header
		}
		ip := net.ParseIP(f[1]).To4()
		if n == 1 {
			if ip == nil || ip.String() != gateway {
				return nil
			}
			continue
		}
		if ip == nil || ip[0] != 192 || ip[1] != 168 {
			break
		}
		hops = append(hops, ip.String())
	}
	return hops
}
