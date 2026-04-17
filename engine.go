//go:build android

package snispoof

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

type Config struct {
	ListenHost  string `json:"LISTEN_HOST"`
	ListenPort  int    `json:"LISTEN_PORT"`
	ConnectIP   string `json:"CONNECT_IP"`
	ConnectPort int    `json:"CONNECT_PORT"`
	FakeSNI     string `json:"FAKE_SNI"`
}

const tplHexBody = "1603010200010001fc030341d5b549d9cd1adfa7296c8418d157dc7b624c842824ff493b9375bb48d34f2b20bf018bcc90a7c89a230094815ad0c15b736e38c01209d72d282cb5e2105328150024130213031301c02cc030c02bc02fcca9cca8c024c028c023c027009f009e006b006700ff0100018f0000000b00090000066d63692e6972000b000403000102000a00160014001d0017001e0019001801000101010201030104002300000010000e000c02683208687474702f312e310016000000170000000d002a0028040305030603080708080809080a080b080408050806040105010601030303010302040205020602002b00050403040303002d00020101003300260024001d0020435bacc4d05f9d41fef44ab3ad55616c36e0613473e2338770efdaa98693d217001500d5"

var tpl []byte

func init() {
	body, err := hex.DecodeString(tplHexBody)
	if err != nil {
		panic(err)
	}
	tpl = append(body, make([]byte, 517-len(body))...)
}

type flowState struct {
	mu       sync.Mutex
	synSeq   uint32
	fake     []byte
	fakeSent bool
	done     chan struct{}
}

type tunEngine struct {
	mu        sync.Mutex
	status    string
	running   bool
	cfg       Config
	connectIP net.IP
	tun       *os.File
	listener  net.Listener
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	writeMu   sync.Mutex
	ports     sync.Map // src ephemeral port (uint16) -> *flowState
}

func newTunEngine() *tunEngine {
	return &tunEngine{status: "stopped"}
}

func (e *tunEngine) setStatus(status string) {
	e.mu.Lock()
	e.status = status
	e.mu.Unlock()
}

func (e *tunEngine) Start(tunFd int, config string) error {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return fmt.Errorf("engine already running")
	}
	e.mu.Unlock()

	cfg, connectIP, err := parseConfig(config)
	if err != nil {
		e.setStatus("error: " + err.Error())
		return err
	}

	tunFile := os.NewFile(uintptr(tunFd), "android-tun")
	if tunFile == nil {
		err := fmt.Errorf("failed to wrap tun fd")
		e.setStatus("error: " + err.Error())
		return err
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", cfg.ListenHost, cfg.ListenPort))
	if err != nil {
		_ = tunFile.Close()
		e.setStatus("error: " + err.Error())
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())

	e.mu.Lock()
	e.running = true
	e.cfg = cfg
	e.connectIP = connectIP
	e.tun = tunFile
	e.listener = ln
	e.cancel = cancel
	e.status = "running"
	e.mu.Unlock()

	e.wg.Add(2)
	go e.packetLoop(ctx)
	go e.acceptLoop(ctx)
	return nil
}

func (e *tunEngine) Stop() error {
	e.mu.Lock()
	if !e.running {
		e.status = "stopped"
		e.mu.Unlock()
		return nil
	}
	cancel := e.cancel
	ln := e.listener
	tun := e.tun
	e.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if ln != nil {
		_ = ln.Close()
	}
	if tun != nil {
		_ = tun.Close()
	}
	e.wg.Wait()

	e.ports.Range(func(key, _ any) bool {
		e.ports.Delete(key)
		return true
	})

	e.mu.Lock()
	e.running = false
	e.listener = nil
	e.tun = nil
	e.cancel = nil
	e.status = "stopped"
	e.mu.Unlock()
	return nil
}

func (e *tunEngine) Status() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status
}

func (e *tunEngine) packetLoop(ctx context.Context) {
	defer e.wg.Done()
	buf := make([]byte, 65535)

	for {
		n, err := e.tun.Read(buf)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			e.setStatus("error: tun read failed: " + err.Error())
			return
		}
		if n < 40 {
			continue
		}
		e.processPacket(append([]byte(nil), buf[:n]...))
	}
}

func (e *tunEngine) processPacket(pkt []byte) {
	if len(pkt) < 20 || pkt[0]>>4 != 4 || pkt[9] != 6 {
		return
	}
	ihl := int(pkt[0]&0x0f) * 4
	if ihl < 20 || len(pkt) < ihl+20 {
		return
	}
	totalLen := int(binary.BigEndian.Uint16(pkt[2:4]))
	if totalLen >= ihl+20 && totalLen <= len(pkt) {
		pkt = pkt[:totalLen]
	}
	tcp := pkt[ihl:]
	dataOff := int(tcp[12]>>4) * 4
	if dataOff < 20 || len(tcp) < dataOff {
		return
	}

	flags := tcp[13]
	plen := len(tcp) - dataOff
	src := net.IP(pkt[12:16]).To4()
	dst := net.IP(pkt[16:20]).To4()
	if src == nil || dst == nil {
		return
	}
	srcPort := binary.BigEndian.Uint16(tcp[0:2])
	dstPort := binary.BigEndian.Uint16(tcp[2:4])

	const (
		fin = 1 << 0
		syn = 1 << 1
		rst = 1 << 2
		ack = 1 << 4
	)

	outbound := dst.Equal(e.connectIP) && dstPort == uint16(e.cfg.ConnectPort)
	inbound := src.Equal(e.connectIP) && srcPort == uint16(e.cfg.ConnectPort)

	if outbound {
		seq := binary.BigEndian.Uint32(tcp[4:8])

		if flags&syn != 0 && flags&ack == 0 {
			e.ports.Store(srcPort, &flowState{
				synSeq: seq,
				fake:   buildClientHello(e.cfg.FakeSNI),
				done:   make(chan struct{}),
			})
			return
		}

		if flags&ack != 0 && flags&(syn|fin|rst) == 0 && plen == 0 {
			v, ok := e.ports.Load(srcPort)
			if !ok {
				return
			}
			ps := v.(*flowState)
			ps.mu.Lock()
			if ps.fakeSent {
				ps.mu.Unlock()
				return
			}
			ps.fakeSent = true
			synSeq := ps.synSeq
			fake := append([]byte(nil), ps.fake...)
			ps.mu.Unlock()

			tplCopy := append([]byte(nil), pkt...)
			go func() {
				time.Sleep(1 * time.Millisecond)
				frame, err := buildFakePacket(tplCopy, synSeq, fake)
				if err != nil {
					e.setStatus("error: fake packet build failed: " + err.Error())
					return
				}
				if err := e.writePacket(frame); err != nil {
					e.setStatus("error: fake packet injection failed: " + err.Error())
				}
			}()
		}
	}

	if inbound {
		ackNum := binary.BigEndian.Uint32(tcp[8:12])
		if flags&ack != 0 && flags&(syn|fin|rst) == 0 && plen == 0 {
			v, ok := e.ports.Load(dstPort)
			if !ok {
				return
			}
			ps := v.(*flowState)
			ps.mu.Lock()
			if ps.fakeSent && ackNum == ps.synSeq+1 {
				select {
				case <-ps.done:
				default:
					close(ps.done)
				}
			}
			ps.mu.Unlock()
		}
	}
}

func (e *tunEngine) writePacket(pkt []byte) error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	_, err := e.tun.Write(pkt)
	return err
}

func (e *tunEngine) acceptLoop(ctx context.Context) {
	defer e.wg.Done()
	for {
		c, err := e.listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			e.setStatus("error: accept failed: " + err.Error())
			return
		}
		go e.handleClient(c)
	}
}

func (e *tunEngine) handleClient(client net.Conn) {
	defer client.Close()

	server, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", e.cfg.ConnectIP, e.cfg.ConnectPort), 5*time.Second)
	if err != nil {
		e.setStatus("error: upstream dial failed: " + err.Error())
		return
	}
	defer server.Close()

	port := uint16(server.LocalAddr().(*net.TCPAddr).Port)
	defer e.ports.Delete(port)

	var ps *flowState
	for i := 0; i < 100; i++ {
		if v, ok := e.ports.Load(port); ok {
			ps = v.(*flowState)
			break
		}
		time.Sleep(1 * time.Millisecond)
	}
	if ps == nil {
		e.setStatus("error: sniffer did not register flow")
		return
	}

	select {
	case <-ps.done:
	case <-time.After(2 * time.Second):
		e.setStatus("error: timeout waiting for ACK confirmation")
		return
	}

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(server, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, server); done <- struct{}{} }()
	<-done
}

func parseConfig(raw string) (Config, net.IP, error) {
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return Config{}, nil, fmt.Errorf("invalid config: %w", err)
	}
	if cfg.ListenHost == "" {
		cfg.ListenHost = "127.0.0.1"
	}
	if cfg.ListenPort <= 0 || cfg.ListenPort > 65535 {
		return Config{}, nil, fmt.Errorf("invalid LISTEN_PORT: %d", cfg.ListenPort)
	}
	if cfg.ConnectPort <= 0 || cfg.ConnectPort > 65535 {
		return Config{}, nil, fmt.Errorf("invalid CONNECT_PORT: %d", cfg.ConnectPort)
	}
	if cfg.FakeSNI == "" {
		return Config{}, nil, fmt.Errorf("FAKE_SNI is required")
	}
	connectIP := net.ParseIP(cfg.ConnectIP).To4()
	if connectIP == nil {
		return Config{}, nil, fmt.Errorf("invalid CONNECT_IP: %s", cfg.ConnectIP)
	}
	return cfg, connectIP, nil
}

func buildClientHello(sni string) []byte {
	if len(sni) > 219 {
		panic("sni too long")
	}
	random := make([]byte, 32)
	sessID := make([]byte, 32)
	keyShare := make([]byte, 32)
	_, _ = rand.Read(random)
	_, _ = rand.Read(sessID)
	_, _ = rand.Read(keyShare)

	sniBytes := []byte(sni)
	padLen := 219 - len(sniBytes)
	out := make([]byte, 0, 517)
	out = append(out, tpl[:11]...)
	out = append(out, random...)
	out = append(out, 0x20)
	out = append(out, sessID...)
	out = append(out, tpl[76:120]...)
	out = be16(out, uint16(len(sniBytes)+5))
	out = be16(out, uint16(len(sniBytes)+3))
	out = append(out, 0x00)
	out = be16(out, uint16(len(sniBytes)))
	out = append(out, sniBytes...)
	out = append(out, tpl[127+6:262+6]...)
	out = append(out, keyShare...)
	out = append(out, 0x00, 0x15)
	out = be16(out, uint16(padLen))
	out = append(out, make([]byte, padLen)...)
	if len(out) != 517 {
		panic(fmt.Sprintf("bad ClientHello size: %d", len(out)))
	}
	return out
}

func be16(b []byte, v uint16) []byte {
	var x [2]byte
	binary.BigEndian.PutUint16(x[:], v)
	return append(b, x[:]...)
}

func buildFakePacket(template []byte, isn uint32, fake []byte) ([]byte, error) {
	if len(template) < 40 {
		return nil, fmt.Errorf("template too short")
	}
	ihl := int(template[0]&0x0f) * 4
	if ihl < 20 || len(template) < ihl+20 {
		return nil, fmt.Errorf("bad IPv4 header")
	}
	tcpHL := int(template[ihl]>>4) * 4
	if tcpHL < 20 || len(template) < ihl+tcpHL {
		return nil, fmt.Errorf("bad TCP header")
	}

	hdrLen := ihl + tcpHL
	out := make([]byte, 0, hdrLen+len(fake))
	out = append(out, template[:hdrLen]...)
	out = append(out, fake...)

	binary.BigEndian.PutUint16(out[2:4], uint16(len(out)))
	id := binary.BigEndian.Uint16(out[4:6])
	binary.BigEndian.PutUint16(out[4:6], id+1)
	out[10], out[11] = 0, 0
	binary.BigEndian.PutUint16(out[10:12], ipChecksum(out[:ihl]))

	out[ihl+13] |= 0x08 // PSH
	seq := isn + 1 - uint32(len(fake))
	binary.BigEndian.PutUint32(out[ihl+4:ihl+8], seq)
	out[ihl+16], out[ihl+17] = 0, 0
	binary.BigEndian.PutUint16(out[ihl+16:ihl+18], tcpChecksum(out[:ihl], out[ihl:]))

	return out, nil
}

func sum16(b []byte) uint32 {
	var s uint32
	for i := 0; i+1 < len(b); i += 2 {
		s += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)&1 == 1 {
		s += uint32(b[len(b)-1]) << 8
	}
	for s>>16 != 0 {
		s = (s & 0xffff) + (s >> 16)
	}
	return s
}

func fold(s uint32) uint16 {
	for s>>16 != 0 {
		s = (s & 0xffff) + (s >> 16)
	}
	return ^uint16(s)
}

func ipChecksum(iph []byte) uint16 {
	return fold(sum16(iph))
}

func tcpChecksum(iph, tcpAndPayload []byte) uint16 {
	pseudo := make([]byte, 12)
	copy(pseudo[0:4], iph[12:16])
	copy(pseudo[4:8], iph[16:20])
	pseudo[9] = 6
	binary.BigEndian.PutUint16(pseudo[10:12], uint16(len(tcpAndPayload)))
	return fold(sum16(pseudo) + sum16(tcpAndPayload))
}
