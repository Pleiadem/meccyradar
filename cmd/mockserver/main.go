package main

import (
	"flag"
	"log"
	"net/http"

	"meccyradar/internal/mockserver"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18080", "listen address")
	card := flag.String("card", "CARD-1", "test card key")
	flag.Parse()

	server := mockserver.New(*card)
	defer server.Close()
	log.Printf("mock management server listening on http://%s", *addr)
	log.Printf("test card key: %s", *card)
	log.Printf("FRP endpoint expected by the mock: 127.0.0.1:7000")
	if err := http.ListenAndServe(*addr, server.Handler()); err != nil {
		log.Fatal(err)
	}
}
