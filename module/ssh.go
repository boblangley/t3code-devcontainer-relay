package t3relay

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
)

const sshTargetPort = 22

type directTCPIPRequest struct {
	Host       string
	Port       uint32
	OriginHost string
	OriginPort uint32
}

func serveTailnetSSH(listener net.Listener, app *RelayApp) {
	signer, err := loadOrCreateSSHSigner(app.SSHHostKeyFile)
	if err != nil {
		app.logger.Error("tailnet ssh host key unavailable", zap.Error(err), zap.String("path", app.SSHHostKeyFile))
		_ = listener.Close()
		return
	}

	config := &ssh.ServerConfig{
		ServerVersion: "SSH-2.0-t3code-relay",
		MaxAuthTries:  3,
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if subtle.ConstantTimeCompare([]byte(conn.User()), []byte(app.SSHAllowedUser)) != 1 {
				return nil, fmt.Errorf("unsupported user %q", conn.User())
			}
			return nil, nil
		},
	}
	config.AddHostKey(signer)

	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			app.logger.Error("tailnet ssh accept failed", zap.Error(err))
			return
		}
		go handleTailnetSSHConn(conn, config, app)
	}
}

func handleTailnetSSHConn(conn net.Conn, config *ssh.ServerConfig, app *RelayApp) {
	defer conn.Close()

	serverConn, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		app.logger.Debug("tailnet ssh rejected", zap.Error(err), zap.String("remote_addr", conn.RemoteAddr().String()))
		return
	}
	defer serverConn.Close()

	go ssh.DiscardRequests(reqs)
	for ch := range chans {
		if ch.ChannelType() != "direct-tcpip" {
			rejectSSHChannel(ch, ssh.UnknownChannelType, "only direct-tcpip forwarding is supported")
			continue
		}
		go handleDirectTCPIPChannel(ch, app, serverConn.RemoteAddr().String())
	}
}

func rejectSSHChannel(ch ssh.NewChannel, reason ssh.RejectionReason, message string) {
	if err := ch.Reject(reason, message); err != nil {
		// Rejection errors are expected when clients disconnect mid-handshake.
		return
	}
}

func handleDirectTCPIPChannel(ch ssh.NewChannel, app *RelayApp, remoteAddr string) {
	var req directTCPIPRequest
	if err := ssh.Unmarshal(ch.ExtraData(), &req); err != nil {
		rejectSSHChannel(ch, ssh.ConnectionFailed, "invalid direct-tcpip request")
		return
	}

	env, err := app.ResolveSSHForwardTarget(req.Host, int(req.Port))
	if err != nil {
		app.logger.Warn("tailnet ssh target rejected",
			zap.String("target_host", req.Host),
			zap.Uint32("target_port", req.Port),
			zap.String("remote_addr", remoteAddr),
			zap.Error(err),
		)
		rejectSSHChannel(ch, ssh.Prohibited, err.Error())
		return
	}

	channel, requests, err := ch.Accept()
	if err != nil {
		app.logger.Debug("tailnet ssh channel accept failed", zap.Error(err))
		return
	}
	defer channel.Close()
	go ssh.DiscardRequests(requests)

	target := net.JoinHostPort(env.IP, fmt.Sprint(app.SSHBackendPort))
	backend, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		app.logger.Warn("tailnet ssh dial target failed",
			zap.String("environment", env.Name),
			zap.String("target", target),
			zap.Error(err),
		)
		return
	}
	defer backend.Close()

	copyDone := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(backend, channel)
		_ = backend.SetDeadline(time.Now())
		copyDone <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(channel, backend)
		_ = channel.CloseWrite()
		copyDone <- struct{}{}
	}()
	<-copyDone
	<-copyDone
}

func (a *RelayApp) ResolveSSHForwardTarget(host string, port int) (Environment, error) {
	if port != sshTargetPort {
		return Environment{}, fmt.Errorf("port %d is not allowed", port)
	}

	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if h, p, err := net.SplitHostPort(host); err == nil {
		host = h
		parsedPort, err := net.LookupPort("tcp", p)
		if err != nil || parsedPort != sshTargetPort {
			return Environment{}, fmt.Errorf("port %q is not allowed", p)
		}
	}

	env, ok := a.LookupByHost(host)
	if !ok {
		return Environment{}, fmt.Errorf("unsupported host %q", host)
	}
	if env.Status == "stopped" {
		return Environment{}, fmt.Errorf("environment %q is stopped", env.Name)
	}
	if env.IP == "" {
		return Environment{}, fmt.Errorf("environment %q has no reachable address", env.Name)
	}
	return env, nil
}

func loadOrCreateSSHSigner(path string) (ssh.Signer, error) {
	if data, err := os.ReadFile(path); err == nil {
		return ssh.ParsePrivateKey(data)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read ssh host key: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir ssh host key dir: %w", err)
	}

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ssh host key: %w", err)
	}
	keyBytes, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("marshal ssh host key: %w", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: keyBytes,
	})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		return nil, fmt.Errorf("write ssh host key: %w", err)
	}
	return ssh.ParsePrivateKey(pemBytes)
}
