package t3relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
	"go.uber.org/zap"
	"tailscale.com/tsnet"
)

var tailnetHTTPSBridgeID atomic.Uint64

type tailnetRuntime struct {
	server *tsnet.Server
	tcp443 net.Listener
	tcp22  net.Listener
	tcp53  net.Listener
	udp53  net.PacketConn
	dnsTCP *dns.Server
	dnsUDP *dns.Server
	ip4    netip.Addr
	ip6    netip.Addr
}

func startTailnet(ctx context.Context, app *RelayApp) (*tailnetRuntime, error) {
	if err := os.MkdirAll(app.TailscaleStateDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir tailscale state dir: %w", err)
	}

	ts := &tsnet.Server{
		Dir:      app.TailscaleStateDir,
		Hostname: app.TailscaleHostname,
		AuthKey:  app.TailscaleAuthKey,
		UserLogf: func(format string, args ...any) {
			app.logger.Info(fmt.Sprintf(format, args...))
		},
		Logf: func(format string, args ...any) {
			app.logger.Debug(fmt.Sprintf(format, args...))
		},
	}

	status, err := ts.Up(ctx)
	if err != nil {
		return nil, fmt.Errorf("tsnet up: %w", err)
	}

	ip4, ip6 := ts.TailscaleIPs()
	runtime := &tailnetRuntime{
		server: ts,
		ip4:    ip4,
		ip6:    ip6,
	}

	app.logger.Info("tailscale node ready",
		withOptionalAddr("tailscale_ipv4", ip4),
		withOptionalAddr("tailscale_ipv6", ip6),
	)
	if status != nil && status.Self != nil {
		app.logger.Info("tailscale self status",
			zap.String("dns_name", status.Self.DNSName),
		)
	}

	if runtime.tcp443, err = ts.Listen("tcp", ":443"); err != nil {
		_ = ts.Close()
		return nil, fmt.Errorf("tsnet listen tcp :443: %w", err)
	}
	go serveTailnetHTTPS(runtime.tcp443, app)

	if runtime.tcp22, err = ts.Listen("tcp", ":22"); err != nil {
		_ = runtime.Close()
		return nil, fmt.Errorf("tsnet listen tcp :22: %w", err)
	}
	go serveTailnetSSH(runtime.tcp22, app)

	dnsHandler := &tailnetDNSHandler{
		app: app,
		ip4: ip4,
		ip6: ip6,
	}

	dnsPacketAddr, ok := tailnetDNSPacketListenAddr(ip4, ip6)
	if !ok {
		_ = runtime.Close()
		return nil, errors.New("tsnet listen udp :53: no tailscale IP available")
	}
	if runtime.udp53, err = ts.ListenPacket("udp", dnsPacketAddr.String()); err != nil {
		_ = runtime.Close()
		return nil, fmt.Errorf("tsnet listen udp :53: %w", err)
	}
	runtime.dnsUDP = &dns.Server{PacketConn: runtime.udp53, Handler: dnsHandler}
	go func() {
		if err := runtime.dnsUDP.ActivateAndServe(); err != nil && !errors.Is(err, net.ErrClosed) {
			app.logger.Error("tailnet udp dns server stopped", zap.Error(err))
		}
	}()

	if runtime.tcp53, err = ts.Listen("tcp", ":53"); err != nil {
		_ = runtime.Close()
		return nil, fmt.Errorf("tsnet listen tcp :53: %w", err)
	}
	runtime.dnsTCP = &dns.Server{Listener: runtime.tcp53, Handler: dnsHandler}
	go func() {
		if err := runtime.dnsTCP.ActivateAndServe(); err != nil && !errors.Is(err, net.ErrClosed) {
			app.logger.Error("tailnet tcp dns server stopped", zap.Error(err))
		}
	}()

	return runtime, nil
}

func (r *tailnetRuntime) Close() error {
	if r == nil {
		return nil
	}
	if r.dnsTCP != nil {
		_ = r.dnsTCP.Shutdown()
	}
	if r.dnsUDP != nil {
		_ = r.dnsUDP.Shutdown()
	}
	if r.tcp443 != nil {
		_ = r.tcp443.Close()
	}
	if r.tcp22 != nil {
		_ = r.tcp22.Close()
	}
	if r.tcp53 != nil {
		_ = r.tcp53.Close()
	}
	if r.udp53 != nil {
		_ = r.udp53.Close()
	}
	if r.server != nil {
		return r.server.Close()
	}
	return nil
}

func serveTailnetHTTPS(listener net.Listener, app *RelayApp) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			app.logger.Error("tailnet https accept failed", zap.Error(err))
			return
		}
		go proxyTailnetHTTPS(conn, app)
	}
}

func proxyTailnetHTTPS(inbound net.Conn, app *RelayApp) {
	defer inbound.Close()

	bridgeID := tailnetHTTPSBridgeID.Add(1)
	started := time.Now()
	app.logger.Info("tailnet https bridge started",
		zap.Uint64("bridge_id", bridgeID),
		zap.String("tailnet_remote_addr", inbound.RemoteAddr().String()),
		zap.String("tailnet_local_addr", inbound.LocalAddr().String()),
	)

	backend, err := net.DialTimeout("tcp", "127.0.0.1:443", 5*time.Second)
	if err != nil {
		app.logger.Error("tailnet https dial local caddy failed",
			zap.Uint64("bridge_id", bridgeID),
			zap.Duration("duration", time.Since(started)),
			zap.Error(err),
		)
		return
	}
	defer backend.Close()

	app.logger.Info("tailnet https bridge connected local caddy",
		zap.Uint64("bridge_id", bridgeID),
		zap.String("caddy_remote_addr", backend.RemoteAddr().String()),
		zap.String("caddy_local_addr", backend.LocalAddr().String()),
	)

	proxyBidirectional(inbound, backend, app.logger, bridgeID)

	app.logger.Info("tailnet https bridge finished",
		zap.Uint64("bridge_id", bridgeID),
		zap.Duration("duration", time.Since(started)),
	)
}

type closeWriter interface {
	CloseWrite() error
}

func proxyBidirectional(client, backend net.Conn, logger *zap.Logger, bridgeID uint64) {
	var wg sync.WaitGroup
	wg.Add(2)
	go copyThenCloseWrite(&wg, backend, client, logger, bridgeID, "tailnet_to_caddy")
	go copyThenCloseWrite(&wg, client, backend, logger, bridgeID, "caddy_to_tailnet")
	wg.Wait()
}

func copyThenCloseWrite(wg *sync.WaitGroup, dst, src net.Conn, logger *zap.Logger, bridgeID uint64, direction string) {
	defer wg.Done()
	started := time.Now()
	bytesCopied, copyErr := io.Copy(dst, src)
	closeErr := closeWrite(dst)

	fields := []zap.Field{
		zap.Uint64("bridge_id", bridgeID),
		zap.String("direction", direction),
		zap.Int64("bytes", bytesCopied),
		zap.Duration("duration", time.Since(started)),
		zap.String("src_remote_addr", src.RemoteAddr().String()),
		zap.String("src_local_addr", src.LocalAddr().String()),
		zap.String("dst_remote_addr", dst.RemoteAddr().String()),
		zap.String("dst_local_addr", dst.LocalAddr().String()),
	}
	if copyErr != nil {
		fields = append(fields, zap.Error(copyErr))
	}
	if closeErr != nil {
		fields = append(fields, zap.NamedError("close_write_error", closeErr))
	}

	if copyErr != nil || closeErr != nil {
		logger.Warn("tailnet https bridge copy finished with error", fields...)
		return
	}
	logger.Info("tailnet https bridge copy finished", fields...)
}

func closeWrite(conn net.Conn) error {
	if cw, ok := conn.(closeWriter); ok {
		return cw.CloseWrite()
	}
	return conn.SetDeadline(time.Now())
}

type tailnetDNSHandler struct {
	app *RelayApp
	ip4 netip.Addr
	ip6 netip.Addr
}

func (h *tailnetDNSHandler) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	resp := new(dns.Msg)
	resp.SetReply(req)
	resp.Authoritative = true
	resp.RecursionAvailable = false

	if len(req.Question) == 0 {
		resp.Rcode = dns.RcodeFormatError
		_ = w.WriteMsg(resp)
		return
	}

	q := req.Question[0]
	name, _, ok := h.app.ParseServedHost(q.Name)
	if !ok || name == "" {
		resp.Rcode = dns.RcodeNameError
		_ = w.WriteMsg(resp)
		return
	}

	switch q.Qtype {
	case dns.TypeA:
		if h.ip4.IsValid() {
			resp.Answer = append(resp.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30},
				A:   net.IP(h.ip4.AsSlice()),
			})
		}
	case dns.TypeAAAA:
		if h.ip6.IsValid() {
			resp.Answer = append(resp.Answer, &dns.AAAA{
				Hdr:  dns.RR_Header{Name: q.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 30},
				AAAA: net.IP(h.ip6.AsSlice()),
			})
		}
	case dns.TypeANY:
		if h.ip4.IsValid() {
			resp.Answer = append(resp.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30},
				A:   net.IP(h.ip4.AsSlice()),
			})
		}
		if h.ip6.IsValid() {
			resp.Answer = append(resp.Answer, &dns.AAAA{
				Hdr:  dns.RR_Header{Name: q.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 30},
				AAAA: net.IP(h.ip6.AsSlice()),
			})
		}
	default:
		// Known name, unsupported type -> NODATA.
	}

	_ = w.WriteMsg(resp)
}

func withOptionalAddr(field string, addr netip.Addr) zap.Field {
	if !addr.IsValid() {
		return zap.String(field, "")
	}
	return zap.String(field, addr.String())
}

func tailnetDNSPacketListenAddr(ip4, ip6 netip.Addr) (netip.AddrPort, bool) {
	if ip4.IsValid() {
		return netip.AddrPortFrom(ip4, 53), true
	}
	if ip6.IsValid() {
		return netip.AddrPortFrom(ip6, 53), true
	}
	return netip.AddrPort{}, false
}
