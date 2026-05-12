package main

import (
	"context"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"

	"github.com/neoromantics/chess/pkg/auth"
	"github.com/neoromantics/chess/pkg/eventbus"
)

type Gateway struct {
	userSvcURL *url.URL
	bus        *eventbus.Client
	hub        *Hub
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	userSvcURLStr := os.Getenv("USER_SERVICE_URL")
	if userSvcURLStr == "" {
		userSvcURLStr = "http://user-service:8081"
	}
	userSvcURL, err := url.Parse(userSvcURLStr)
	if err != nil {
		log.Fatalf("invalid user service URL: %v", err)
	}

	redisAddr := os.Getenv("REDIS_URL")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}
	bus := eventbus.NewClient(redisAddr)
	hub := NewHub(bus)

	gw := &Gateway{
		userSvcURL: userSvcURL,
		bus:        bus,
		hub:        hub,
	}

	// Start WebSocket Hub
	go hub.Run(context.Background())

	mux := http.NewServeMux()

	// 1. Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("Gateway OK"))
	})

	// 2. Reverse proxy to User Service
	userProxy := httputil.NewSingleHostReverseProxy(userSvcURL)
	mux.HandleFunc("/api/auth/", userProxy.ServeHTTP)
	mux.HandleFunc("/api/user/", userProxy.ServeHTTP)

	// 3. WebSockets (Placeholders for now)
	mux.HandleFunc("/ws", gw.handleWSGame)
	mux.HandleFunc("/ws/user", gw.handleWSUser)

	// 4. Static Assets & SPA Routing
	mux.HandleFunc("/", gw.handleIndex)

	log.Printf("Gateway Service starting on port %s...", port)
	log.Fatal(http.ListenAndServe(":"+port, auth.Middleware(mux)))
}

