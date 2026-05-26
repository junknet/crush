//go:build ignore

package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func main() {
	url := "ws://127.0.0.1:9091"
	token := "ymm_rpc_2026"

	nc, err := nats.Connect(url, nats.Token(token))
	if err != nil {
		log.Fatal(err)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	kv, err := js.KeyValue(ctx, "CRUSH_SESSIONS")
	if err != nil {
		log.Fatal("Failed to get KV: ", err)
	}

	keys, err := kv.Keys(ctx)
	if err != nil {
		log.Fatal("Failed to get keys: ", err)
	}

	fmt.Printf("Found %d local sessions:\n", len(keys))
	for _, k := range keys {
		v, _ := kv.Get(ctx, k)
		if v != nil {
			fmt.Printf("- %s: %s\n", k, string(v.Value()))
		}
	}
}
