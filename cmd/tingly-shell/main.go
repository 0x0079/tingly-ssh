// Command tingly-shell is the SSH-over-QUIC bridge: a client that accepts
// local TCP or stdio, a server that dials sshd, and a resumable session layer
// in between. See docs/01-architecture.md.
package main

import (
	"context"
	"crypto/tls"
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

	"golang.org/x/crypto/ssh"

	"github.com/0x0079/tingly-shell/internal/auth"
	"github.com/0x0079/tingly-shell/internal/bridge"
	"github.com/0x0079/tingly-shell/internal/proto"
	"github.com/0x0079/tingly-shell/internal/transport"
)

const usage = `tingly-shell - SSH over QUIC with session resumption

Usage:
  tingly-shell server  --listen :7443 --target 127.0.0.1:22 --authorized-keys FILE [--credentials FILE]
  tingly-shell proxy   --server HOST:7443                      # SSH keys from ssh-agent, server key trusted on first use
  tingly-shell client  --server HOST:7443 --listen 127.0.0.1:2222
  tingly-shell proxy   --server HOST:7443 --token-file FILE --pin sha256:...   # token instead of keys
  tingly-shell keygen --label laptop-mbp14 [--expires 2027-06-01]
  tingly-shell version

Modes:
  server   run next to sshd and bridge incoming streams to --target
  client   listen on a local TCP port; "ssh -p 2222 user@127.0.0.1"
  proxy    bridge stdin/stdout, for ssh -o ProxyCommand='tingly-shell proxy ...'
  keygen   mint a credential: a token for one device plus its server record
  version  print the build version

Signals:
  SIGUSR1  (client, proxy) drop the current link and reconnect, e.g. after a network change
  SIGHUP   (server) reload the credentials and authorized keys files; revoking is deleting a line

Run "tingly-shell <mode> -h" for the flags of a mode.
`

// version is overridden at release build time via
// -ldflags "-X main.version=...". See .goreleaser.yaml.
var version = "dev"

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
		err = runKeygen(os.Args[2:])
	case "version", "-v", "--version":
		fmt.Println("tingly-shell " + version)
		return
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
		listen     = fs.String("listen", ":7443", "UDP address to listen on")
		targets    = fs.String("target", "127.0.0.1:22", "comma separated allowlist of TCP targets; the first is the default")
		credFile   = fs.String("credentials", "", "file of client credentials: 'label sha256:<base64> [not-after]' per line")
		keysFile   = fs.String("authorized-keys", "", "OpenSSH authorized_keys file of SSH keys (and cert-authority lines) allowed in")
		tokenFile  = fs.String("token-file", "", "deprecated alias for --credentials; a file holding one raw shared token")
		stateDir   = fs.String("state-dir", defaultStateDir("server"), "directory for the generated self-signed certificate")
		certFile   = fs.String("cert", "", "TLS certificate (default: <state-dir>/cert.pem, generated if absent)")
		keyFile    = fs.String("key", "", "TLS private key (default: <state-dir>/key.pem)")
		hosts      = fs.String("cert-hosts", "", "comma separated DNS names or IPs for the generated certificate")
		maxSess    = fs.Int("max-sessions", bridge.DefaultMaxSessions, "concurrent sessions to hold; bounds server memory")
		maxPerCred = fs.Int("max-sessions-per-credential", 0, "per-credential session cap; 0 means unlimited")
	)
	var common commonFlags
	common.bind(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch {
	case *credFile != "" && *tokenFile != "":
		return errors.New("pass either --credentials or --token-file, not both")
	case *credFile == "" && *tokenFile != "":
		*credFile = *tokenFile
	case *credFile == "" && *keysFile == "":
		return errors.New("--authorized-keys or --credentials is required: list users' SSH public keys, or mint tokens with `tingly-shell keygen --label <device>`")
	}
	var creds *auth.Store
	if *credFile != "" {
		var err error
		if creds, err = auth.LoadStore(*credFile); err != nil {
			return err
		}
	}
	var keys *auth.KeyStore
	if *keysFile != "" {
		var err error
		if keys, err = auth.LoadKeyStore(*keysFile); err != nil {
			return err
		}
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

	cfg := bridge.ServerConfig{
		Listen:                   *listen,
		Targets:                  splitList(*targets),
		Credentials:              creds,
		Keys:                     keys,
		Window:                   common.window,
		MaxStreams:               common.maxStreams,
		MaxSessions:              *maxSess,
		MaxSessionsPerCredential: *maxPerCred,
		Linger:                   common.linger,
		Tuning:                   common.tuning(),
		TLS:                      transport.ServerTLS(cert),
		Logger:                   log,
	}
	srv, err := bridge.NewServer(cfg)
	if err != nil {
		return err
	}
	defer srv.Close()
	if created {
		log.Info("generated self-signed certificate", "cert", *certFile, "key", *keyFile)
	}
	log.Info("server listening", "addr", srv.Addr().String(), "targets", *targets, "pin", pin)
	// Worst case memory is a function of the three limits; print it so the
	// host can be sized instead of guessed (docs/04-security-model.md §8, R-3).
	log.Info("session limits",
		"max_sessions", cfg.MaxSessions,
		"max_streams_per_session", common.maxStreams,
		"window_bytes", common.window,
		"worst_case_memory_mib", cfg.WorstCaseMemory()/(1<<20))
	switch {
	case creds == nil:
	case creds.Shared():
		log.Warn("credentials file holds one shared token: no per-device identity, revocation or attribution",
			"fix", "give each device its own credential, see docs/adr/0004-client-identity.md")
	default:
		log.Info("credentials loaded", "count", creds.Len(), "labels", strings.Join(creds.Labels(), ","))
	}
	if keys != nil {
		log.Info("authorized keys loaded", "count", keys.Len(), "labels", strings.Join(keys.Labels(), ","))
	}
	log.Info("clients must pass this pin", "flag", "--pin "+pin)
	go reloadOnSignal(ctx, srv, log)
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
		tokenFile  = fs.String("token-file", "", "authenticate with this pre-shared token (mode 0600) instead of SSH keys")
		identity   = fs.String("identity", "", "SSH key to authenticate with: a private key, or a .pub whose key is in ssh-agent (default: every ssh-agent key)")
		knownFile  = fs.String("known-servers", defaultKnownServers(), "server keys trusted on first use, when authenticating with SSH keys and no --pin")
		pin        = fs.String("pin", "", "expected server key pin, sha256:<base64>; required with --token-file unless the server has a CA-signed certificate")
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
	if *tokenFile != "" && *identity != "" {
		return errors.New("pass either --token-file or --identity, not both")
	}
	if *serverName == "" {
		host, _, splitErr := net.SplitHostPort(*server)
		if splitErr != nil {
			return fmt.Errorf("--server must be host:port: %w", splitErr)
		}
		*serverName = host
	}
	log := common.logger()

	var (
		token   []byte
		prover  *auth.Prover
		tlsConf *tls.Config
		err     error
	)
	if *tokenFile != "" {
		if token, err = transport.LoadToken(*tokenFile); err != nil {
			return err
		}
	} else {
		if prover, err = auth.NewAgentProver(os.Getenv("SSH_AUTH_SOCK"), *identity); err != nil {
			return err
		}
		// Fail now, not after a linger's worth of retries, when there is no
		// key to sign with.
		pubs, err := prover.Keys()
		if err != nil {
			return err
		}
		fps := make([]string, len(pubs))
		for i, p := range pubs {
			fps[i] = ssh.FingerprintSHA256(p)
		}
		log.Debug("authenticating with SSH keys", "source", prover.String(), "keys", strings.Join(fps, ","))
	}
	switch {
	case *pin != "" || *insecure:
		if *pin == "" && token != nil {
			log.Warn("certificate verification disabled: the pre-shared token can be stolen by a man in the middle; use --pin")
		}
		tlsConf, err = transport.ClientTLS(*serverName, *pin, *insecure)
	case token != nil:
		// A token is a secret, so it is never sent to a server trusted on
		// first use; without a pin the system trust store applies.
		tlsConf, err = transport.ClientTLS(*serverName, "", false)
	default:
		// SSH key proofs are bound to the connection and useless to anyone
		// else, so trusting the server key on first use is safe
		// (.design/ssh-key-auth.pencil.md §4).
		known := transport.NewKnownServers(*knownFile)
		tlsConf, err = transport.ClientTLSTrustOnFirstUse(*serverName, *server, known, func(got string) {
			log.Warn("trusting this server key from now on", "server", *server, "pin", got, "file", known.Path())
		})
	}
	if err != nil {
		return err
	}

	cfg := bridge.ClientConfig{
		Server:     *server,
		Target:     *target,
		Token:      token,
		Window:     common.window,
		MaxStreams: common.maxStreams,
		Linger:     common.linger,
		Tuning:     common.tuning(),
		TLS:        tlsConf,
		Logger:     log,
	}
	if prover != nil {
		cfg.Keys = prover
	}
	cli, err := bridge.NewClient(cfg)
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

// reloadOnSignal re-reads the credentials file on SIGHUP, so revoking a device
// does not need a restart (and therefore does not disturb other sessions).
func reloadOnSignal(ctx context.Context, srv *bridge.Server, log *slog.Logger) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	defer signal.Stop(ch)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			if err := srv.ReloadCredentials(); err != nil {
				// The previous set stays in place, so a typo cannot lock
				// everyone out.
				log.Error("credentials reload failed, keeping the previous set", "err", err)
			}
		}
	}
}

func runKeygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	label := fs.String("label", "", "device label to identify this credential (recommended)")
	expires := fs.String("expires", "", "expiry date, YYYY-MM-DD or RFC 3339 (default: never)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var notAfter time.Time
	if *expires != "" {
		t, err := auth.ParseNotAfter(*expires)
		if err != nil {
			return err
		}
		notAfter = t
	}
	tok, err := transport.NewToken()
	if err != nil {
		return err
	}
	// The token goes to stdout so `keygen > token` still works; the record the
	// operator has to install goes to stderr.
	fmt.Println(tok)
	name := *label
	if name == "" {
		name = "unnamed-device"
	}
	fmt.Fprintf(os.Stderr, `
Give the token above to the device: write it to a file with mode 0600 and pass
--token-file to the client.

Add this line to the server's --credentials file (it holds only a hash, so the
server never stores the secret), then send the server SIGHUP:

%s

`, auth.Record(name, []byte(tok), notAfter))
	if *label == "" {
		fmt.Fprintln(os.Stderr, "tip: pass --label <device> so logs and revocation can name this device")
	}
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

// defaultKnownServers sits next to the role directories rather than inside
// one: it is the user's record of servers, like ~/.ssh/known_hosts.
func defaultKnownServers() string {
	return filepath.Join(filepath.Dir(defaultStateDir("client")), "known_servers")
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
