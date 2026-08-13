package busclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestSDKSmokeDualKernel(t *testing.T) {
	kind := os.Getenv("BUSCLIENT_SMOKE_KERNEL")
	if kind == "" {
		t.Skip("set BUSCLIENT_SMOKE_KERNEL=go or python to run external-kernel smoke")
	}
	port := freePort(t)
	process := startSmokeKernel(t, kind, port)
	defer func() { stopSmokeKernel(process) }()
	waitForPort(t, port)
	url := fmt.Sprintf("ws://127.0.0.1:%d/ws", port)

	fast := []Option{WithBackoff(50*time.Millisecond, 200*time.Millisecond)}
	caller := New(url, testManifest("smoke-caller"), fast...)
	responder := New(url, testManifest("smoke-responder"), fast...)
	publisher := New(url, testManifest("smoke-publisher"), fast...)
	subscriber := New(url, testManifest("smoke-subscriber"), WithBackoff(400*time.Millisecond, 400*time.Millisecond))
	clients := []*Client{caller, responder, publisher, subscriber}
	defer func() {
		for _, client := range clients {
			_ = client.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, client := range clients {
		if err := client.Connect(ctx); err != nil {
			t.Fatalf("hello: %v", err)
		}
	}
	t.Logf("hello: PASS (%s kernel)", kind)

	if err := publisher.Set(ctx, "smoke:_:state", map[string]any{"sequence": float64(1)}); err != nil {
		t.Fatal(err)
	}
	received := make(chan Frame, 8)
	if _, err := subscriber.Subscribe("smoke:_:state", func(frame Frame) { received <- frame }); err != nil {
		t.Fatal(err)
	}
	frame := receiveSmokeFrame(t, received)
	if frame.Type != "set" || frame.Value.(map[string]any)["sequence"] != float64(1) {
		t.Fatalf("retained frame = %#v", frame)
	}
	t.Log("subscribe + retained replay: PASS")

	if _, err := subscriber.Subscribe("smoke:_:event", func(frame Frame) { received <- frame }); err != nil {
		t.Fatal(err)
	}
	if err := publisher.Publish(ctx, "smoke:_:event", map[string]any{"fanout": true}); err != nil {
		t.Fatal(err)
	}
	frame = receiveSmokeFrame(t, received)
	if frame.Type != "publish" || frame.Value.(map[string]any)["fanout"] != true {
		t.Fatalf("fanout frame = %#v", frame)
	}
	t.Log("publish fanout: PASS")

	if _, err := responder.Subscribe("smoke:_:rpc", func(frame Frame) {
		value := frame.Value.(map[string]any)
		if value["_cancel"] == true {
			return
		}
		_ = responder.Publish(context.Background(), value["_reply_to"].(string), map[string]any{
			"_corr": value["_corr"], "ok": true, "result": map[string]any{"echo": value["message"]},
		})
	}); err != nil {
		t.Fatal(err)
	}
	result, err := caller.Request(ctx, "smoke:_:rpc", map[string]any{"message": "roundtrip"}, 2*time.Second)
	if err != nil || result.(map[string]any)["echo"] != "roundtrip" {
		t.Fatalf("RPC result=%#v err=%v", result, err)
	}
	t.Log("RPC roundtrip: PASS")

	_, err = caller.Request(ctx, "nobody:_:request", nil, 80*time.Millisecond)
	if !errors.Is(err, ErrRequestTimeout) {
		t.Fatalf("timeout error = %v", err)
	}
	t.Log("request timeout: PASS")

	oldConn := subscriber.Conn()
	stopSmokeKernel(process)
	waitUntil(t, 3*time.Second, func() bool { return !subscriber.Connected() })
	process = startSmokeKernel(t, kind, port)
	waitForPort(t, port)
	seed := New(url, testManifest("smoke-seed"), WithReconnect(false))
	clients = append(clients, seed)
	seedCtx, seedCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer seedCancel()
	if err := seed.Connect(seedCtx); err != nil {
		t.Fatal(err)
	}
	if err := seed.Set(seedCtx, "smoke:_:state", map[string]any{"sequence": float64(2)}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 6*time.Second, func() bool { return subscriber.Connected() && subscriber.Conn() != oldConn })
	frame = receiveSmokeFrame(t, received)
	if frame.Type != "set" || frame.Value.(map[string]any)["sequence"] != float64(2) {
		t.Fatalf("reconnect replay = %#v", frame)
	}
	if err := seed.Publish(seedCtx, "smoke:_:event", map[string]any{"after_restart": true}); err != nil {
		t.Fatal(err)
	}
	frame = receiveSmokeFrame(t, received)
	if frame.Value.(map[string]any)["after_restart"] != true {
		t.Fatalf("restored live subscription = %#v", frame)
	}
	t.Log("kernel kill + reconnect + subscription restore + retained replay: PASS")
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func startSmokeKernel(t *testing.T, kind string, port int) *exec.Cmd {
	t.Helper()
	var command *exec.Cmd
	switch kind {
	case "go":
		command = exec.Command("/tmp/viewerd", "--host", "127.0.0.1", "--port", fmt.Sprint(port))
	case "python":
		repoRoot, err := filepath.Abs("../../..")
		if err != nil {
			t.Fatal(err)
		}
		nextDir := filepath.Join(repoRoot, "next")
		command = exec.Command(filepath.Join(nextDir, ".venv", "bin", "python"), "-m", "kernel", "--host", "127.0.0.1", "--port", fmt.Sprint(port))
		command.Dir = nextDir
	default:
		t.Fatalf("unknown BUSCLIENT_SMOKE_KERNEL %q", kind)
	}
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	return command
}

func stopSmokeKernel(command *exec.Cmd) {
	if command == nil || command.Process == nil || command.ProcessState != nil {
		return
	}
	_ = command.Process.Kill()
	_ = command.Wait()
}

func waitForPort(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	address := fmt.Sprintf("127.0.0.1:%d", port)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("kernel did not listen on %s", address)
}

func receiveSmokeFrame(t *testing.T, frames <-chan Frame) Frame {
	t.Helper()
	select {
	case frame := <-frames:
		return frame
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for bus frame")
	}
	return Frame{}
}
