package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/neoromantics/chess/pkg/eventbus"
)

func main() {
	redisAddr := os.Getenv("REDIS_URL")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	bus := eventbus.NewClient(redisAddr)
	hostname, _ := os.Hostname()
	log.Printf("Game Service [%s] starting (Command Processor)...", hostname)

	ctx := context.Background()
	for {
		msgs, err := bus.ReadCommands(ctx, "game-service-group", hostname, 5*time.Second)
		if err != nil {
			log.Printf("Error reading commands: %v", err)
			time.Sleep(1 * time.Second)
			continue
		}

		for _, msg := range msgs {
			log.Printf("Received command: %s", msg.ID)
			// Implementation to come in Phase 3
			bus.Ack(ctx, eventbus.StreamGameCommands, "game-service-group", msg.ID)
		}
	}
}
