// Command tingly-shell is the SSH-over-QUIC bridge: a client that accepts
// local TCP or stdio, a server that dials sshd, and a resumable session layer
// in between. See docs/01-architecture.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/0x0079/tingly-shell/internal/bridge"
	"github.com/0x0079/tingly-shell/internal/proto"
	"github.com/0x0079/tingly-shell/internal/transport"
)

const usage = `tingly-shell - SSH over QUIC with session resumption

Usage:
  tingly-shell server  --listen :7443 --target 127.0.0.1:22 --token-file FILE
  tingly-shell client  --server HOST:7443 --listen 127.0.0.1:2222 --token-file FILE --pin sha256:...
  tingly-shell proxy   --server HOST:7443 --token-file FILE --pin sha256:...
  tingly-shell keygen

Modes:
  server   run next to sshd and bridge incoming streams to --target
  client   listen on a local TCP port; "ssh -p 2222 user@127.0.0.1"
  proxy    bridge stdin/stdout, for ssh -o ProxyCommand='tingly-shell proxy ...'
  keygen   print a fresh pre-shared token

Signals (client and proxy):
  SIGUSR1  drop the current link and reconnect, e.g. after a network change

Run "tingly-shell <mode> -h" for the flags of a mode.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "server":
		err = runServer(ctx, os.Args[2:])
	case "client":
		err = runClient(ctx, os.Args[2:], false)
	case "proxy":
		err = runClient(ctx, os.Args[2:], true)
	case "keygen":
		err = runKeygen()
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "tingly-shell: %v\n", err)
		os.Exit(1)
	}
}

// common flags shared by every mode that talks QUIC.
type commonFlags struct {
	window     uint64
	maxStreams int
	linger     time.Duration
	keepAlive  time.Duration
	idle       time.Duration
	logLevel   string
}

func (c *commonFlags) bind(fs *flag.FlagSet) {
	fs.Uint64Var(&c.window, "window", proto.DefaultWindow, "per-stream receive window in bytes (also bounds replay memory)")
	fs.IntVar(&c.maxStreams, "max-streams", 64, "maximum concurrent logical streams per session")
	fs.DurationVar(&c.linger, "session-linger", 60*time.Second, "how long a session survives with no link before it is abandoned")
	fs.DurationVar(&c.keepAlive, "keepalive", 5*time.Second, "QUIC keepalive period")
	fs.DurationVar(&c.idle, "idle-timeout", 20*time.Second, "QUIC idle timeout; a dead path is detected after this long")
	fs.StringVar(&c.logLevel, "log-level", "info", "log level: debug, info, warn, error")
}

func (c *commonFlags) logger() *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(c.logLevel)); err != nil {
		level = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

func (c *commonFlags) tuning() transport.Tuning {
	return transport.Tuning{KeepAlive: c.keepAlive, IdleTimeout: c.idle, Window: c.window}
}

func runServer(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	var (
		listen    = fs.String("listen", ":7443", "UDP address to listen on")
		targets   = fs.String("target", "127.0.0.1:22", "comma separated allowlist of TCP targets; the first is the default")
		tokenFile = fs.String("token-file", "", "file holding the pre-shared token (required, mode 0600)")
		stateDir  = fs.String("state-dir", defaultStateDir("server"), "directory for the generated self-signed certificate")
		certFile  = fs.String("cert", "", "TLS certificate (default: <state-dir>/cert.pem, generated if absent)")
		keyFile   = fs.String("key", "", "TLS private key (default: <state-dir>/key.pem)")
		hosts     = fs.String("cert-hosts", "", "comma separated DNS names or IPs for the generated certificate")
	)
	var common commonFlags
	common.bind(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *tokenFile == "" {
		return errors.New("--token-file is required; create one with `tingly-shell keygen`")
	}
	token, err := transport.LoadToken(*tokenFile)
	if err != nil {
		return err
	}
	if *certFile == "" {
		*certFile = filepath.Join(*stateDir, "cert.pem")
	}
	if *keyFile == "" {
		*keyFile = filepath.Join(*stateDir, "key.pem")
	}
	cert, created, err := transport.LoadOrCreateCert(*certFile, *keyFile, splitList(*hosts))
	if err != nil {
		return err
	}
	pin, err := transport.PinOf(cert)
	if err != nil {
		return err
	}
	log := common.logger()

	srv, err := bridge.NewServer(bridge.ServerConfig{
		Listen:     *listen,
		Targets:    splitList(*targets),
		Token:      token,
		Window:     common.window,
		MaxStreams: common.maxStreams,
		Linger:     common.linger,
		Tuning:     common.tuning(),
		TLS:        transport.ServerTLS(cert),
		Logger:     log,
	})
	if err != nil {
		return err
	}
	defer srv.Close()
	if created {
		log.Info("generated self-signed certificate", "cert", *certFile, "key", *keyFile)
	}
	log.Info("server listening", "addr", srv.Addr().String(), "targets", *targets, "pin", pin)
	log.Info("clients must pass this pin", "flag", "--pin "+pin)
	return srv.Serve(ctx)
}

func runClient(ctx context.Context, args []string, stdioMode bool) error {
	name := "client"
	if stdioMode {
		name = "proxy"
	}
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	var (
		server     = fs.String("server", "", "tingly-shell server address, host:port (required)")
		listen     = fs.String("listen", "127.0.0.1:2222", "local TCP address to accept ssh on (client mode only)")
		target     = fs.String("target", "127.0.0.1:22", "target hint sent to the server")
		tokenFile  = fs.String("token-file", "", "file holding the pre-shared token (required, mode 0600)")
		pin        = fs.String("pin", "", "expected server key pin, sha256:<base64> (strongly recommended)")
		serverName = fs.String("server-name", "", "TLS server name; defaults to the --server host")
		insecure   = fs.Bool("insecure", false, "skip server certificate verification (development only)")
	)
	var common commonFlags
	common.bind(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *server == "" {
		return errors.New("--server is required")
	}
	if *tokenFile == "" {
		return errors.New("--token-file is required")
	}
	token, err := transport.LoadToken(*tokenFile)
	if err != nil {
		return err
	}
	if *serverName == "" {
		host, _, splitErr := net.SplitHostPort(*server)
		if splitErr != nil {
			return fmt.Errorf("--server must be host:port: %w", splitErr)
		}
		*serverName = host
	}
	log := common.logger()
	if *pin == "" && *insecure {
		log.Warn("certificate verification disabled: the pre-shared token can be stolen by a man in the middle; use --pin")
	}
	tlsConf, err := transport.ClientTLS(*serverName, *pin, *insecure)
	if err != nil {
		return err
	}

	cli, err := bridge.NewClient(bridge.ClientConfig{
		Server:     *server,
		Target:     *target,
		Token:      token,
		Window:     common.window,
		MaxStreams: common.maxStreams,
		Linger:     common.linger,
		Tuning:     common.tuning(),
		TLS:        tlsConf,
		Logger:     log,
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- cli.Run(ctx) }()
	go dropLinkOnSignal(ctx, cli, log)

	serveErr := make(chan error, 1)
	if stdioMode {
		go func() { serveErr <- cli.ServeStdio(ctx) }()
	} else {
		ln, err := net.Listen("tcp", *listen)
		if err != nil {
			return err
		}
		defer ln.Close()
		log.Info("client listening", "addr", ln.Addr().String(), "server", *server, "target", *target)
		log.Info("connect with", "cmd", fmt.Sprintf("ssh -p %s user@%s", port(ln.Addr()), host(ln.Addr())))
		go func() { serveErr <- cli.ServeListener(ctx, ln) }()
	}

	select {
	case err := <-serveErr:
		// In stdio mode the bridged stream ending is a normal exit.
		cancel()
		<-runErr
		return err
	case err := <-runErr:
		return err
	}
}

// dropLinkOnSignal re-homes the session when SIGUSR1 arrives. A supervisor
// script can send it after the OS reports a network change, instead of waiting
// for the old path to time out; the verification suite uses it to inject link
// failures into a live ssh session.
func dropLinkOnSignal(ctx context.Context, cli *bridge.Client, log *slog.Logger) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)
	defer signal.Stop(ch)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			if cli.Session().DropLink() {
				log.Warn("dropped link on SIGUSR1, reconnecting")
			} else {
				log.Warn("SIGUSR1 received but no link is attached")
			}
		}
	}
}

func runKeygen() error {
	tok, err := transport.NewToken()
	if err != nil {
		return err
	}
	fmt.Println(tok)
	fmt.Fprintln(os.Stderr, "write this to a file with mode 0600 on both ends, then pass --token-file")
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func defaultStateDir(role string) string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "tingly-shell", role)
	}
	return filepath.Join(".", ".tingly-shell", role)
}

func host(a net.Addr) string {
	h, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return a.String()
	}
	return h
}

func port(a net.Addr) string {
	_, p, err := net.SplitHostPort(a.String())
	if err != nil {
		return ""
	}
	return p
}
