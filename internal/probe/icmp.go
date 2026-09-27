package probe

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

const payloadLen = 56 // same as the system ping

// ICMP pings one IPv4 address.
//
// It prefers an unprivileged ICMP datagram socket (no sudo needed on macOS)
// and falls back to a raw socket where that is not allowed, e.g. on Linux
// running as root with ping_group_range disabled.
//
// Each request carries a random per-source token and our own 64-bit sequence
// number in its payload. Replies are matched on the token rather than the ICMP
// identifier (Linux rewrites the identifier on datagram sockets, and on macOS
// every ICMP socket sees every echo reply), and the payload sequence avoids the
// 16-bit wrap of the ICMP one.
type ICMP struct {
	Addr     *net.IPAddr
	Interval time.Duration
}

func NewICMP(host string, interval time.Duration) (*ICMP, error) {
	addr, err := net.ResolveIPAddr("ip4", host)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	return &ICMP{Addr: addr, Interval: interval}, nil
}

func (p *ICMP) Run(ctx context.Context, target int, out chan<- Event) error {
	conn, privileged, err := listen()
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		return err
	}
	id := int(binary.BigEndian.Uint16(token[:2]))

	var dst net.Addr = p.Addr
	if !privileged {
		dst = &net.UDPAddr{IP: p.Addr.IP}
	}

	go p.receive(ctx, conn, target, token, out)

	ticker := time.NewTicker(p.Interval)
	defer ticker.Stop()
	for seq := int64(0); ; seq++ {
		payload := make([]byte, payloadLen)
		copy(payload, token[:])
		binary.BigEndian.PutUint64(payload[8:], uint64(seq))
		msg := icmp.Message{
			Type: ipv4.ICMPTypeEcho,
			Body: &icmp.Echo{ID: id, Seq: int(seq & 0xffff), Data: payload},
		}
		b, err := msg.Marshal(nil)
		if err != nil {
			return err
		}
		if !emit(ctx, out, Event{Target: target, Kind: Sent, Seq: seq, Time: time.Now()}) {
			return nil
		}
		// A send error (no route, interface down) is what an outage looks
		// like from here; the request simply never gets a reply.
		_, _ = conn.WriteTo(b, dst)

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (p *ICMP) receive(ctx context.Context, conn *icmp.PacketConn, target int, token [8]byte, out chan<- Event) {
	buf := make([]byte, 1500)
	for {
		n, _, err := conn.ReadFrom(buf)
		now := time.Now()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		msg, err := icmp.ParseMessage(1, buf[:n])
		if err != nil || msg.Type != ipv4.ICMPTypeEchoReply {
			continue
		}
		echo, ok := msg.Body.(*icmp.Echo)
		if !ok || len(echo.Data) < 16 || string(echo.Data[:8]) != string(token[:]) {
			continue
		}
		seq := int64(binary.BigEndian.Uint64(echo.Data[8:16]))
		if !emit(ctx, out, Event{Target: target, Kind: Reply, Seq: seq, Time: now}) {
			return
		}
	}
}

func listen() (conn *icmp.PacketConn, privileged bool, err error) {
	conn, err = icmp.ListenPacket("udp4", "0.0.0.0")
	if err == nil {
		return conn, false, nil
	}
	conn, rawErr := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if rawErr == nil {
		return conn, true, nil
	}
	if errors.Is(err, os.ErrPermission) {
		return nil, false, fmt.Errorf("cannot open an ICMP socket (%v); try running with sudo", err)
	}
	return nil, false, fmt.Errorf("open ICMP socket: %w", err)
}

func emit(ctx context.Context, out chan<- Event, e Event) bool {
	select {
	case out <- e:
		return true
	case <-ctx.Done():
		return false
	}
}
