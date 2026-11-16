package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func startCDPTestServer(t *testing.T, handle func(context.Context, *websocket.Conn)) string {
	t.Helper()
	var wg sync.WaitGroup
	wg.Add(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		go func() {
			defer wg.Done()
			defer conn.Close(websocket.StatusNormalClosure, "test complete")
			handle(context.Background(), conn)
		}()
	}))
	t.Cleanup(func() {
		server.Close()
		wg.Wait()
	})
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

func writeCDPTestMessage(t *testing.T, conn *websocket.Conn, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(context.Background(), websocket.MessageText, data); err != nil {
		t.Fatal(err)
	}
}

func TestCDPCallAndEventDemultiplexing(t *testing.T) {
	url := startCDPTestServer(t, func(ctx context.Context, conn *websocket.Conn) {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var request map[string]any
		if json.Unmarshal(data, &request) != nil {
			return
		}
		writeCDPTestMessage(t, conn, map[string]any{"method": "Page.loadEventFired", "params": map[string]any{"timestamp": 123}})
		writeCDPTestMessage(t, conn, map[string]any{"id": request["id"], "result": map[string]any{"answer": 42}})
	})
	conn, err := dialCDP(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.close()
	result, err := conn.call(context.Background(), "Runtime.evaluate", map[string]any{}, nil)
	if err != nil || !strings.Contains(string(result), `"answer":42`) {
		t.Fatalf("call result = %s, %v", result, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	event, err := conn.nextEvent(ctx)
	if err != nil || event.Method != "Page.loadEventFired" || !strings.Contains(string(event.Params), `"timestamp":123`) {
		t.Fatalf("event = %+v, %v", event, err)
	}
}

func TestCDPEventQueueOverflowFailsConnection(t *testing.T) {
	url := startCDPTestServer(t, func(ctx context.Context, conn *websocket.Conn) {
		for index := 0; index < cdpEventQueueSize+1; index++ {
			if err := conn.Write(ctx, websocket.MessageText, []byte(fmt.Sprintf(`{"method":"Target.targetCreated","params":{"index":%d}}`, index))); err != nil {
				return
			}
		}
		_, _, _ = conn.Read(ctx)
	})
	conn, err := dialCDP(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.close()
	select {
	case <-conn.done:
	case <-time.After(2 * time.Second):
		t.Fatal("event overflow did not close the connection")
	}
	conn.mu.Lock()
	err = conn.err
	conn.mu.Unlock()
	if err == nil || !strings.Contains(err.Error(), "event queue overflow") {
		t.Fatalf("connection error = %v", err)
	}
}

func TestCDPCloseFailsPendingCall(t *testing.T) {
	url := startCDPTestServer(t, func(ctx context.Context, conn *websocket.Conn) {
		_, _, _ = conn.Read(ctx)
		_ = conn.Close(websocket.StatusNormalClosure, "stop")
	})
	conn, err := dialCDP(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.close()
	_, err = conn.call(context.Background(), "Runtime.evaluate", map[string]any{}, nil)
	if err == nil {
		t.Fatal("pending call survived connection close")
	}
}
