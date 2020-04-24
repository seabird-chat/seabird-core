package main

import (
	"log"

	seabird "github.com/belak/seabird-core"
)

func main() {
	server, err := seabird.NewServer()
	if err != nil {
		log.Fatalf("failed to create server: %v", err)
	}

	err = server.ListenAndServe()
	if err != nil {
		log.Fatalf("failed to start server: %v", err)
	}
}
