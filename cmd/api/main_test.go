package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestServeGracefullyDrainsRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		listener, client := pipeHTTP(t)
		entered := make(chan struct{})
		release := make(chan struct{})
		defer close(release)
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			_, _ = io.WriteString(w, "finished")
		})}
		defer server.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		served := make(chan error, 1)
		go func() { served <- serve(ctx, server, listener, 3*time.Second, testLogger()) }()
		response := make(chan error, 1)
		go func() {
			res, err := client.Get("http://test/")
			if err != nil {
				response <- err
				return
			}
			defer res.Body.Close()
			body, err := io.ReadAll(res.Body)
			if err == nil && string(body) != "finished" {
				err = errors.New("in-flight response did not finish")
			}
			response <- err
		}()
		<-entered
		shutdownStarted := make(chan struct{})
		server.RegisterOnShutdown(func() { close(shutdownStarted) })
		cancel()
		<-shutdownStarted
		select {
		case err := <-served:
			t.Fatalf("shutdown returned before request finished: %v", err)
		default:
		}
		release <- struct{}{}
		if err := <-response; err != nil {
			t.Fatal(err)
		}
		if err := <-served; err != nil {
			t.Fatal(err)
		}
	})
}

func TestServeReturnsListenerFailure(t *testing.T) {
	listener, _ := pipeHTTP(t)
	_ = listener.Close()
	err := serve(context.Background(), &http.Server{}, listener, time.Second, testLogger())
	if err == nil {
		t.Fatal("listener failure must be reported")
	}
}

func TestServeForcesCloseAfterShutdownDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		listener, client := pipeHTTP(t)
		entered := make(chan struct{})
		requestCanceled := make(chan struct{})
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			<-r.Context().Done()
			close(requestCanceled)
		})}
		defer server.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		served := make(chan error, 1)
		go func() { served <- serve(ctx, server, listener, time.Second, testLogger()) }()
		clientDone := make(chan struct{})
		go func() {
			defer close(clientDone)
			res, err := client.Get("http://test/")
			if err == nil {
				res.Body.Close()
			}
		}()
		<-entered
		cancel()
		if err := <-served; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected bounded shutdown failure, got %v", err)
		}
		<-requestCanceled
		<-clientDone
	})
}

// net.Pipe exercises HTTP and shutdown without opening operating-system sockets.
type pipeListener struct {
	connections chan net.Conn
	closed      chan struct{}
	once        sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.connections:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddress{} }

type pipeAddress struct{}

func (pipeAddress) Network() string { return "pipe" }
func (pipeAddress) String() string  { return "test" }

func pipeHTTP(t *testing.T) (*pipeListener, *http.Client) {
	t.Helper()
	listener := &pipeListener{connections: make(chan net.Conn), closed: make(chan struct{})}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		client, server := net.Pipe()
		select {
		case listener.connections <- server:
			return client, nil
		case <-ctx.Done():
			client.Close()
			server.Close()
			return nil, ctx.Err()
		case <-listener.closed:
			client.Close()
			server.Close()
			return nil, net.ErrClosed
		}
	}}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		listener.Close()
	})
	return listener, client
}
