package main

import (
	"log"
	"os"
)

func main() {
	hostname, _ := os.Hostname()
	log.Printf("Matchmaker Service [%s] starting...", hostname)
	
	// Main loop to be implemented in Phase 4
	select {}
}
