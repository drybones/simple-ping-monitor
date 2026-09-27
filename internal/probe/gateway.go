package probe

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
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
