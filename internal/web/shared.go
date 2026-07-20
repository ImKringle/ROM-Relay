package web

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// AdvertiseIP resolves the address ROM Relay advertises to clients. A configured
// value wins; otherwise a specific listen address is used directly, and a
// wildcard listen falls back to the single private LAN adapter (ambiguous when
// several exist).
func AdvertiseIP(configured, listen string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	if ip := net.ParseIP(listen); ip != nil && !ip.IsUnspecified() {
		return ip.String(), nil
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	seen := map[string]bool{}
	for _, a := range addrs {
		ip, _, e := net.ParseCIDR(a.String())
		if e == nil && ip.To4() != nil && ip.IsPrivate() {
			seen[ip.String()] = true
		}
	}
	if len(seen) != 1 {
		var ips []string
		for ip := range seen {
			ips = append(ips, ip)
		}
		sort.Strings(ips)
		return "", fmt.Errorf("LAN adapter selection is ambiguous (%s); set advertise_ip", strings.Join(ips, ", "))
	}
	for ip := range seen {
		return ip, nil
	}
	return "", nil
}

func tokenQuery(r *http.Request) string {
	if t := r.URL.Query().Get("token"); t != "" {
		return "?token=" + url.QueryEscape(t)
	}
	return ""
}

// sendFBIQueue delivers a newline-terminated URL list to FBI's port-5000
// receiver and waits (off the request path) for its post-install acknowledgement.
func sendFBIQueue(addr string, payload []byte) error {
	if len(payload) == 0 || uint64(len(payload)) > uint64(^uint32(0)) {
		return errors.New("invalid queue payload size")
	}
	conn, err := net.DialTimeout("tcp4", addr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}
	if err := conn.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
		conn.Close()
		return err
	}
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame, uint32(len(payload)))
	copy(frame[4:], payload)
	for len(frame) > 0 {
		n, writeErr := conn.Write(frame)
		if writeErr != nil {
			conn.Close()
			return fmt.Errorf("send queue: %w", writeErr)
		}
		if n == 0 {
			conn.Close()
			return io.ErrShortWrite
		}
		frame = frame[n:]
	}
	// FBI sends its acknowledgement only after every queued installation has
	// finished. Keep the socket alive without holding the browser request open.
	go func() {
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(48 * time.Hour))
		var ack [1]byte
		io.ReadFull(conn, ack[:])
	}()
	return nil
}
