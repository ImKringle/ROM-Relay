package web

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

func TestSendFBIQueueProtocol(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	want := []byte("http://192.168.1.10:8080/d/a\nhttp://192.168.1.10:8080/d/b\n")
	received := make(chan []byte, 1)
	errs := make(chan error, 1)
	allowAck := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			errs <- err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		var size uint32
		if err := binary.Read(conn, binary.BigEndian, &size); err != nil {
			errs <- err
			return
		}
		payload := make([]byte, size)
		if _, err := io.ReadFull(conn, payload); err != nil {
			errs <- err
			return
		}
		received <- payload
		<-allowAck
		if _, err := conn.Write([]byte{0}); err != nil {
			errs <- err
			return
		}
	}()
	sent := make(chan error, 1)
	go func() { sent <- sendFBIQueue(listener.Addr().String(), want) }()
	select {
	case err := <-errs:
		t.Fatal(err)
	case got := <-received:
		if !bytes.Equal(got, want) {
			t.Fatalf("payload=%q want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for queue payload")
	}
	select {
	case err := <-sent:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("sender waited for FBI's completion acknowledgement")
	}
	close(allowAck)
}

func TestAdvertiseIP(t *testing.T) {
	ip, err := AdvertiseIP("192.168.1.5", "0.0.0.0")
	if err != nil || ip != "192.168.1.5" {
		t.Fatalf("%q %v", ip, err)
	}
	ip, err = AdvertiseIP("", "127.0.0.1")
	if err != nil || ip != "127.0.0.1" {
		t.Fatalf("%q %v", ip, err)
	}
}
