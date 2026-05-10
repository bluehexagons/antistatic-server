package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func sendStunBindingRequest(t *testing.T, target *net.UDPAddr) (txn []byte, response []byte, srcPort int) {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("client listen: %v", err)
	}
	defer conn.Close()

	txn = make([]byte, stunTransactionIDLength)
	for i := range txn {
		txn[i] = byte(i*7 + 13)
	}
	req := make([]byte, stunHeaderLength)
	binary.BigEndian.PutUint16(req[0:2], stunBindingRequestType)
	binary.BigEndian.PutUint16(req[2:4], 0)
	binary.BigEndian.PutUint32(req[4:8], stunMagicCookie)
	copy(req[8:], txn)

	if _, err := conn.WriteToUDP(req, target); err != nil {
		t.Fatalf("client write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, stunMaxPacketLength)
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("client read: %v", err)
	}
	return txn, buf[:n], conn.LocalAddr().(*net.UDPAddr).Port
}

func TestStunBindingResponse(t *testing.T) {
	srv, err := startStunServer("127.0.0.1", 0)
	if err != nil {
		t.Fatalf("startStunServer: %v", err)
	}
	defer srv.Close(context.Background())

	target := srv.localAddr().(*net.UDPAddr)

	txn, resp, srcPort := sendStunBindingRequest(t, target)

	if len(resp) < stunHeaderLength+12 {
		t.Fatalf("response too short: %d", len(resp))
	}
	if got := binary.BigEndian.Uint16(resp[0:2]); got != stunBindingSuccessType {
		t.Fatalf("type = %#x, want %#x", got, stunBindingSuccessType)
	}
	if got := binary.BigEndian.Uint32(resp[4:8]); got != stunMagicCookie {
		t.Fatalf("cookie = %#x, want %#x", got, stunMagicCookie)
	}
	if !bytes.Equal(resp[8:stunHeaderLength], txn) {
		t.Fatalf("transaction id mismatch: got %x want %x", resp[8:stunHeaderLength], txn)
	}
	if got := binary.BigEndian.Uint16(resp[stunHeaderLength:stunHeaderLength+2]); got != stunXorMappedAddrType {
		t.Fatalf("attr type = %#x, want XOR-MAPPED-ADDRESS", got)
	}
	body := resp[stunHeaderLength+4:]
	if body[1] != stunIPv4Family {
		t.Fatalf("family = %#x, want IPv4", body[1])
	}
	port := binary.BigEndian.Uint16(body[2:4]) ^ uint16(stunMagicCookie>>16)
	if int(port) != srcPort {
		t.Fatalf("port = %d, want %d (source port reflected)", port, srcPort)
	}
	addr := binary.BigEndian.Uint32(body[4:8]) ^ stunMagicCookie
	if addr != 0x7f000001 {
		t.Fatalf("addr = %#x, want 127.0.0.1", addr)
	}
}

func TestStunIgnoresNonBindingPackets(t *testing.T) {
	srv, err := startStunServer("127.0.0.1", 0)
	if err != nil {
		t.Fatalf("startStunServer: %v", err)
	}
	defer srv.Close(context.Background())

	target := srv.localAddr().(*net.UDPAddr)

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer conn.Close()

	// Garbage / wrong-cookie packet should not get a reply.
	junk := []byte("not a stun packet at all here")
	if _, err := conn.WriteToUDP(junk, target); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	buf := make([]byte, stunMaxPacketLength)
	if n, _, err := conn.ReadFromUDP(buf); err == nil {
		t.Fatalf("expected timeout, got %d bytes", n)
	}
}
