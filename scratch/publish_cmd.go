//go:build ignore

package main

import (
	"encoding/json"
	"github.com/nats-io/nats.go"
	"log"
	"os"
)

type Command struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

func main() {
	natsURL := "nats://47.110.255.240:4222"
	token := "ymm_rpc_2026"
	sessionID := "4152ad3b-ca67-402f-a84d-38331d4d4520"
	text := "sleep 10"
	if len(os.Args) > 1 {
		text = os.Args[1]
	}

	nc, err := nats.Connect(natsURL, nats.Token(token))
	if err != nil {
		log.Fatalf("NATS connect failed: %v", err)
	}
	defer nc.Close()

	cmd := Command{
		Type: "prompt",
		Text: text,
	}
	data, err := json.Marshal(cmd)
	if err != nil {
		log.Fatalf("Marshal failed: %v", err)
	}

	subject := "crush.sess." + sessionID + ".cmd"
	if err := nc.Publish(subject, data); err != nil {
		log.Fatalf("Publish failed: %v", err)
	}

	log.Printf("Successfully published cmd %q to %s", text, subject)
}
