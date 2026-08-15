// pingpong demonstrates two bus clients calling each other over RPC.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"viewer/sdk/go/busclient"
)

func manifest(id string) busclient.Manifest {
	return busclient.Manifest{
		ID: id, Version: "1.0.0",
		Slots: map[string]any{"ping": map[string]any{"channel": id + ":_:ping"}},
		Emits: map[string]any{},
	}
}

func main() {
	url := flag.String("url", "ws://127.0.0.1:8765/ws", "kernel WebSocket URL")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ping := busclient.New(*url, manifest("ping"))
	pong := busclient.New(*url, manifest("pong"))
	defer ping.Close()
	defer pong.Close()

	respond := func(client *busclient.Client, name string) func(busclient.Frame) {
		return func(frame busclient.Frame) {
			value, ok := frame.Value.(map[string]any)
			if !ok || value["_cancel"] == true {
				return
			}
			replyTo, _ := value["_reply_to"].(string)
			corr, _ := value["_corr"].(string)
			_ = client.Publish(context.Background(), replyTo, map[string]any{
				"_corr": corr, "ok": true,
				"result": fmt.Sprintf("%s received %v", name, value["message"]),
			})
		}
	}
	if _, err := ping.Subscribe("ping:_:ping", respond(ping, "ping")); err != nil {
		log.Fatal(err)
	}
	if _, err := pong.Subscribe("pong:_:ping", respond(pong, "pong")); err != nil {
		log.Fatal(err)
	}
	if err := ping.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	if err := pong.Connect(ctx); err != nil {
		log.Fatal(err)
	}

	fromPing, err := ping.Request(ctx, "pong:_:ping", map[string]any{"message": "ping"})
	if err != nil {
		log.Fatal(err)
	}
	fromPong, err := pong.Request(ctx, "ping:_:ping", map[string]any{"message": "pong"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(fromPing)
	fmt.Println(fromPong)
}
