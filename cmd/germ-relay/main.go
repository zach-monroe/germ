// Command germ-relay is the relay for germ session sharing: it pairs a
// host with guests and forwards end-to-end encrypted traffic it can't
// read. It's pure Go (no cgo), so it builds and deploys anywhere.
//
//	germ-relay -addr :8080
//
// Put it behind TLS (Fly.io, Caddy, nginx, ...); germ links point at its
// public https URL.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/zach-monroe/germ/internal/share"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()
	if p := os.Getenv("PORT"); p != "" && !isFlagSet("addr") {
		*addr = ":" + p
	}
	srv := &http.Server{
		Addr:              *addr,
		Handler:           share.NewRelay().Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("germ relay listening on %s", *addr)
	log.Fatal(srv.ListenAndServe())
}

func isFlagSet(name string) bool {
	set := false
	flag.Visit(func(f *flag.Flag) { set = set || f.Name == name })
	return set
}
