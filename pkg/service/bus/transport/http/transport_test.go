package http

import (
	"net"
	stdhttp "net/http"
	"testing"

	"github.com/54c1/niq/core/event"
	bussvc "github.com/54c1/niq/pkg/service/bus"
)

func TestHttpTransport_PublishLoopback(t *testing.T) {
	b := bussvc.NewBus()
	_ = b.RegisterWorker("w1", []string{"e1"}, []string{"*"})
	transport := NewHttpTransport(b, "")
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	srv := &stdhttp.Server{Handler: transport.Handler()}
	go srv.Serve(l)
	defer srv.Close()
	client := NewHttpClient("http://"+l.Addr().String(), "w1", "")
	if err := client.Publish(event.New("e1", "w1", map[string]any{"msg": "hello"})); err != nil {
		t.Fatalf("Publish: %v", err)
	}
}

func TestHttpTransport_PublishACLRejected(t *testing.T) {
	b := bussvc.NewBus()
	_ = b.RegisterWorker("w1", []string{"e1"}, []string{"*"})
	transport := NewHttpTransport(b, "")
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	srv := &stdhttp.Server{Handler: transport.Handler()}
	go srv.Serve(l)
	defer srv.Close()
	client := NewHttpClient("http://"+l.Addr().String(), "w1", "")
	if err := client.Publish(event.New("e2", "w1", nil)); err == nil {
		t.Fatal("expected ACL error")
	}
}

func TestHttpTransport_PublishSpoofingRejected(t *testing.T) {
	b := bussvc.NewBus()
	_ = b.RegisterWorker("w1", []string{"e1"}, []string{"*"})
	transport := NewHttpTransport(b, "")
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	srv := &stdhttp.Server{Handler: transport.Handler()}
	go srv.Serve(l)
	defer srv.Close()
	client := NewHttpClient("http://"+l.Addr().String(), "w1", "")
	if err := client.Publish(event.New("e1", "w2", nil)); err == nil {
		t.Fatal("expected spoofing error")
	}
}

func TestHttpTransport_Subscribe_NoError(t *testing.T) {
	b := bussvc.NewBus()
	_ = b.RegisterWorker("w1", []string{"*"}, []string{"e.*"})
	_, _ = b.BindChannel("w1")
	transport := NewHttpTransport(b, "")
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	srv := &stdhttp.Server{Handler: transport.Handler()}
	go srv.Serve(l)
	defer srv.Close()
	client := NewHttpClient("http://"+l.Addr().String(), "w1", "")
	if err := client.Subscribe([]event.EventPattern{{Type: "e.*"}}); err != nil {
		t.Fatalf("Subscribe should succeed but got: %v", err)
	}
}

func TestHttpTransport_ControlPlane_LoopbackAllowed(t *testing.T) {
	b := bussvc.NewBus()
	transport := NewHttpTransport(b, "")
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	srv := &stdhttp.Server{Handler: transport.Handler()}
	go srv.Serve(l)
	defer srv.Close()
	client := NewHttpClient("http://"+l.Addr().String(), "w1", "")
	if err := client.post("/register", map[string]any{
		"worker_id": "newworker", "publish_allow": []string{"e1"}, "subscribe_allow": []string{"e1"},
	}); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if err := b.Publish("newworker", event.New("e1", "newworker", nil)); err != nil {
		t.Fatalf("Publish as newworker: %v", err)
	}
}
