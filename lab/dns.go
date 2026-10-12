package lab

import (
	"encoding/binary"
	"io"
	"net"
	"strings"
	"sync"

	"golang.org/x/net/dns/dnsmessage"
)

// Lab DNS names. Everything else under .lab.test resolves to 127.0.0.1.
const (
	NameNXDomain  = "nxdomain.lab.test"
	NameServFail  = "servfail.lab.test"
	NameNoAddress = "noaddr.lab.test"
	NameTimeout   = "timeout.lab.test"
	NameBlackhole = "blackhole.lab.test" // 192.0.2.1 (TEST-NET-1)
	NameUnreach   = "unreachable.lab.test"
	NameDualStack = "dual.lab.test" // ::1 and 127.0.0.1
)

// BlackholeIP is a TEST-NET-1 address; the lab dialer drops connections to it.
var BlackholeIP = net.IPv4(192, 0, 2, 1)

// UnreachableIP is a TEST-NET-2 address; the lab dialer reports it unreachable.
var UnreachableIP = net.IPv4(198, 51, 100, 1)

// DNSServer answers queries on loopback for the failure lab.
type DNSServer struct {
	Addr string
	udp  net.PacketConn
	tcp  net.Listener
	wg   sync.WaitGroup
	// Broken makes every query SERVFAIL (simulates a broken local resolver).
	mu     sync.Mutex
	broken bool
}

// StartDNS listens on 127.0.0.1 with the same port for UDP and TCP.
func StartDNS(addr string) (*DNSServer, error) {
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	udp, err := net.ListenPacket("udp", addr)
	if err != nil {
		return nil, err
	}
	tcp, err := net.Listen("tcp", udp.LocalAddr().String())
	if err != nil {
		udp.Close()
		return nil, err
	}
	s := &DNSServer{Addr: udp.LocalAddr().String(), udp: udp, tcp: tcp}
	s.wg.Add(2)
	go s.serveUDP()
	go s.serveTCP()
	return s, nil
}

// SetBroken toggles the "every query fails" mode.
func (s *DNSServer) SetBroken(b bool) {
	s.mu.Lock()
	s.broken = b
	s.mu.Unlock()
}

// Close stops the server.
func (s *DNSServer) Close() {
	s.udp.Close()
	s.tcp.Close()
	s.wg.Wait()
}

func (s *DNSServer) serveUDP() {
	defer s.wg.Done()
	buf := make([]byte, 1500)
	for {
		n, addr, err := s.udp.ReadFrom(buf)
		if err != nil {
			return
		}
		if resp := s.answer(buf[:n]); resp != nil {
			_, _ = s.udp.WriteTo(resp, addr)
		}
	}
}

func (s *DNSServer) serveTCP() {
	defer s.wg.Done()
	for {
		conn, err := s.tcp.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer conn.Close()
			for {
				var size [2]byte
				if _, err := io.ReadFull(conn, size[:]); err != nil {
					return
				}
				msg := make([]byte, binary.BigEndian.Uint16(size[:]))
				if _, err := io.ReadFull(conn, msg); err != nil {
					return
				}
				resp := s.answer(msg)
				if resp == nil {
					return
				}
				out := binary.BigEndian.AppendUint16(nil, uint16(len(resp)))
				if _, err := conn.Write(append(out, resp...)); err != nil {
					return
				}
			}
		}()
	}
}

// answer builds the response for one query, or nil to stay silent (timeout).
func (s *DNSServer) answer(query []byte) []byte {
	var p dnsmessage.Parser
	h, err := p.Start(query)
	if err != nil {
		return nil
	}
	q, err := p.Question()
	if err != nil {
		return nil
	}
	name := strings.ToLower(strings.TrimSuffix(q.Name.String(), "."))
	rh := dnsmessage.Header{ID: h.ID, Response: true, RecursionDesired: h.RecursionDesired,
		RecursionAvailable: true, RCode: dnsmessage.RCodeSuccess}
	var ips []net.IP
	s.mu.Lock()
	broken := s.broken
	s.mu.Unlock()
	switch {
	case broken:
		rh.RCode = dnsmessage.RCodeServerFailure
	case name == NameTimeout:
		return nil
	case name == NameNXDomain || !strings.HasSuffix(name, ".lab.test") && name != "lab.test":
		rh.RCode = dnsmessage.RCodeNameError
	case name == NameServFail:
		rh.RCode = dnsmessage.RCodeServerFailure
	case name == NameNoAddress:
	case name == NameBlackhole:
		ips = []net.IP{BlackholeIP}
	case name == NameUnreach:
		ips = []net.IP{UnreachableIP}
	case name == NameDualStack:
		ips = []net.IP{net.IPv6loopback, net.IPv4(127, 0, 0, 1)}
	default:
		ips = []net.IP{net.IPv4(127, 0, 0, 1)}
	}
	b := dnsmessage.NewBuilder(nil, rh)
	b.EnableCompression()
	_ = b.StartQuestions()
	_ = b.Question(q)
	_ = b.StartAnswers()
	for _, ip := range ips {
		rr := dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: 5}
		if v4 := ip.To4(); v4 != nil && q.Type == dnsmessage.TypeA {
			_ = b.AResource(rr, dnsmessage.AResource{A: [4]byte(v4)})
		} else if v4 == nil && q.Type == dnsmessage.TypeAAAA {
			_ = b.AAAAResource(rr, dnsmessage.AAAAResource{AAAA: [16]byte(ip.To16())})
		}
	}
	out, err := b.Finish()
	if err != nil {
		return nil
	}
	return out
}
