// Package pgtest runs a PostgreSQL 18 server for integration tests: one
// container per test package, in a Podman store the run owns, removed with
// the store when the package's tests end. Each test gets its own database.
package pgtest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Image is the official PostgreSQL 18 image, pinned by its index digest.
const Image = "docker.io/library/postgres:18.6@sha256:3a4264520e121176619f54ff867d12774bc81e95e7b84db58d63bccdb6144b81"

// Owner is the superuser role the server's container creates; it owns the
// store's schema, as the server's own role does on a target.
const Owner = "cliapiproxy"

const (
	commandTimeout = 5 * time.Minute
	readyTimeout   = 2 * time.Minute
)

// Server is one running PostgreSQL server.
type Server struct {
	root      string
	container string
	port      int
	password  string

	mu        sync.Mutex
	templates map[string]*template
}

type template struct {
	once sync.Once
	err  error
}

// Start pulls the image into a new run-owned Podman store and starts the
// server on a free loopback port, waiting until it accepts connections.
func Start(ctx context.Context) (*Server, error) {
	root, err := os.MkdirTemp("", "pgtest-")
	if err != nil {
		return nil, err
	}
	s := &Server{root: root, password: randomHex(16), templates: map[string]*template{}}
	if err = s.start(ctx); err != nil {
		s.Stop()
		return nil, err
	}
	return s, nil
}

func (s *Server) start(ctx context.Context) error {
	port, err := freePort()
	if err != nil {
		return err
	}
	s.port = port
	s.container = "pgtest-" + randomHex(6)
	if _, err = s.podman(ctx, "pull", "--quiet", Image); err != nil {
		return err
	}
	if _, err = s.podman(ctx, "run", "--detach", "--name", s.container, "--network", "host",
		"--env", "POSTGRES_USER="+Owner, "--env", "POSTGRES_PASSWORD="+s.password,
		Image, "-c", "listen_addresses=127.0.0.1", "-c", "port="+strconv.Itoa(port), "-c", "fsync=off"); err != nil {
		return err
	}
	deadline := time.Now().Add(readyTimeout)
	for {
		// The image's first start runs a socket-only server while it
		// initializes, so a TCP connection reaches the final server.
		conn, errConnect := pgx.Connect(ctx, s.URL("postgres", Owner, s.password))
		if errConnect == nil {
			_ = conn.Close(ctx)
			return nil
		}
		if time.Now().After(deadline) {
			logs, _ := s.podman(context.Background(), "logs", s.container)
			return fmt.Errorf("PostgreSQL did not accept connections within %s: %v\n%s", readyTimeout, errConnect, logs)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Stop removes the container and the run's Podman store.
func (s *Server) Stop() {
	if s.container != "" {
		_, _ = s.podman(context.Background(), "rm", "--force", "--time", "0", s.container)
	}
	// Rootless Podman stores files of other subordinate IDs; unshare
	// removes them as their owner.
	_, _ = s.podmanRaw(context.Background(), "unshare", "rm", "-rf", s.root)
	_ = os.RemoveAll(s.root)
}

// URL is a connection URL for database as role with password.
func (s *Server) URL(database, role, password string) string {
	u := url.URL{Scheme: "postgres", User: url.UserPassword(role, password), Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(s.port)), Path: "/" + database}
	u.RawQuery = "sslmode=disable"
	return u.String()
}

// Host and Port name the server's listener; Password is the owner's.
func (s *Server) Host() string     { return "127.0.0.1" }
func (s *Server) Port() int        { return s.port }
func (s *Server) Password() string { return s.password }

// Database creates an empty database the test owns and drops it when the
// test ends; it returns the database's name.
func (s *Server) Database(t testing.TB) string {
	t.Helper()
	return s.create(t, "")
}

// Clone creates a database the test owns from the template name, which
// prepare builds once per server on first use, and drops it when the test
// ends.
func (s *Server) Clone(t testing.TB, name string, prepare func(ctx context.Context, pool *pgxpool.Pool) error) string {
	t.Helper()
	s.mu.Lock()
	tmpl, ok := s.templates[name]
	if !ok {
		tmpl = &template{}
		s.templates[name] = tmpl
	}
	s.mu.Unlock()
	tmpl.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		if tmpl.err = s.exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); tmpl.err != nil {
			return
		}
		pool, err := pgxpool.New(ctx, s.URL(name, Owner, s.password))
		if err != nil {
			tmpl.err = err
			return
		}
		tmpl.err = prepare(ctx, pool)
		pool.Close()
	})
	if tmpl.err != nil {
		t.Fatalf("prepare template %s: %v", name, tmpl.err)
	}
	return s.create(t, name)
}

func (s *Server) create(t testing.TB, template string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	name := "test_" + randomHex(8)
	statement := "CREATE DATABASE " + pgx.Identifier{name}.Sanitize()
	if template != "" {
		statement += " TEMPLATE " + pgx.Identifier{template}.Sanitize()
	}
	if err := s.exec(ctx, statement); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		if err := s.exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
	})
	return name
}

// Pool connects to database as the owner and closes the pool when the test
// ends.
func (s *Server) Pool(t testing.TB, database string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), s.URL(database, Owner, s.password))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func (s *Server) exec(ctx context.Context, statement string) error {
	conn, err := pgx.Connect(ctx, s.URL("postgres", Owner, s.password))
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.Background()) }()
	_, err = conn.Exec(ctx, statement)
	return err
}

// podman runs Podman against the run's own store.
func (s *Server) podman(ctx context.Context, args ...string) (string, error) {
	base := []string{
		"--root", filepath.Join(s.root, "storage"), "--runroot", filepath.Join(s.root, "run"),
		"--tmpdir", filepath.Join(s.root, "tmp"), "--storage-driver", "overlay",
		"--cgroup-manager", "cgroupfs", "--events-backend", "file",
	}
	return s.podmanRaw(ctx, append(base, args...)...)
}

func (s *Server) podmanRaw(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "podman", args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("podman %s: %w\n%s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, errors.New("listener has no TCP address")
	}
	return addr.Port, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Main runs a package's tests with one server, started before and removed
// after them, and exits with their result.
func Main(m *testing.M, server **Server) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	s, err := Start(ctx)
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pgtest: start PostgreSQL: %v\n", err)
		os.Exit(1)
	}
	*server = s
	code := m.Run()
	s.Stop()
	os.Exit(code)
}
