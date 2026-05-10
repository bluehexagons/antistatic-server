package main

import (
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"net"
	"sync"
)

// Minimal RFC 5389 STUN responder. The lobby server speaks just enough STUN
// to let clients discover their externally observed UDP (ip, port) without
// going through a third-party STUN service. We handle Binding Request and
// reply with a Binding Success containing only the XOR-MAPPED-ADDRESS
// attribute, which is all the antistatic client needs (see
// readStunMappedAddress in app/src/engine/networking.ts).
const (
	stunMagicCookie         uint32 = 0x2112a442
	stunBindingRequestType  uint16 = 0x0001
	stunBindingSuccessType  uint16 = 0x0101
	stunHeaderLength               = 20
	stunTransactionIDLength        = 12
	stunXorMappedAddrType   uint16 = 0x0020
	stunIPv4Family          byte   = 0x01
	stunIPv6Family          byte   = 0x02
	stunMaxPacketLength            = 1500
)

type stunServer struct {
	conn     *net.UDPConn
	wg       sync.WaitGroup
	closeOnce sync.Once
}

func startStunServer(host string, port int) (*stunServer, error) {
	if port < 0 || port > 65535 {
		return nil, errors.New("invalid STUN port")
	}
	addr := &net.UDPAddr{Port: port}
	if host != "" {
		ip := net.ParseIP(host)
		if ip == nil {
			return nil, errors.New("invalid STUN host")
		}
		addr.IP = ip
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, err
	}
	s := &stunServer{conn: conn}
	s.wg.Add(1)
	go s.serve()
	return s, nil
}

func (s *stunServer) localAddr() net.Addr {
	if s == nil || s.conn == nil {
		return nil
	}
	return s.conn.LocalAddr()
}

func (s *stunServer) serve() {
	defer s.wg.Done()
	buf := make([]byte, stunMaxPacketLength)
	for {
		n, raddr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// Transient errors (e.g. ICMP-induced ECONNREFUSED on Linux UDP)
			// should not stop the server; just keep reading.
			slog.Debug("STUN read error", "error", err)
			continue
		}
		s.handlePacket(buf[:n], raddr)
	}
}

func (s *stunServer) handlePacket(b []byte, raddr *net.UDPAddr) {
	if len(b) < stunHeaderLength {
		return
	}
	if binary.BigEndian.Uint16(b[0:2]) != stunBindingRequestType {
		return
	}
	if binary.BigEndian.Uint32(b[4:8]) != stunMagicCookie {
		return
	}
	txn := b[8:stunHeaderLength]
	resp, ok := buildBindingSuccess(txn, raddr)
	if !ok {
		return
	}
	if _, err := s.conn.WriteToUDP(resp, raddr); err != nil {
		slog.Debug("STUN write error", "error", err, "remote", raddr.String())
	}
}

// buildBindingSuccess constructs a STUN Binding Success response containing a
// single XOR-MAPPED-ADDRESS attribute that reflects raddr back to the caller
// (per RFC 5389 §15.2).
func buildBindingSuccess(txn []byte, raddr *net.UDPAddr) ([]byte, bool) {
	if len(txn) != stunTransactionIDLength {
		return nil, false
	}
	v4 := raddr.IP.To4()
	addrLen := 4
	family := stunIPv4Family
	if v4 == nil {
		ip6 := raddr.IP.To16()
		if ip6 == nil {
			return nil, false
		}
		addrLen = 16
		family = stunIPv6Family
	}

	// Attribute body = 1 byte reserved + 1 byte family + 2 bytes XOR'd port +
	// addrLen bytes XOR'd address.
	attrBodyLen := 4 + addrLen
	out := make([]byte, stunHeaderLength+4+attrBodyLen)

	binary.BigEndian.PutUint16(out[0:2], stunBindingSuccessType)
	binary.BigEndian.PutUint16(out[2:4], uint16(4+attrBodyLen))
	binary.BigEndian.PutUint32(out[4:8], stunMagicCookie)
	copy(out[8:stunHeaderLength], txn)

	binary.BigEndian.PutUint16(out[stunHeaderLength:stunHeaderLength+2], stunXorMappedAddrType)
	binary.BigEndian.PutUint16(out[stunHeaderLength+2:stunHeaderLength+4], uint16(attrBodyLen))

	body := out[stunHeaderLength+4:]
	body[0] = 0
	body[1] = family
	port := uint16(raddr.Port) ^ uint16(stunMagicCookie>>16)
	binary.BigEndian.PutUint16(body[2:4], port)

	if v4 != nil {
		v4u := binary.BigEndian.Uint32(v4) ^ stunMagicCookie
		binary.BigEndian.PutUint32(body[4:8], v4u)
	} else {
		ip6 := raddr.IP.To16()
		// First 4 bytes XOR with magic cookie, remaining 12 bytes XOR with
		// the transaction ID.
		body[4] = ip6[0] ^ byte((stunMagicCookie>>24)&0xff)
		body[5] = ip6[1] ^ byte((stunMagicCookie>>16)&0xff)
		body[6] = ip6[2] ^ byte((stunMagicCookie>>8)&0xff)
		body[7] = ip6[3] ^ byte(stunMagicCookie&0xff)
		for i := 0; i < 12; i++ {
			body[8+i] = ip6[4+i] ^ txn[i]
		}
	}
	return out, true
}

func (s *stunServer) Close(ctx context.Context) {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		_ = s.conn.Close()
		done := make(chan struct{})
		go func() {
			s.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-ctx.Done():
		}
	})
}
