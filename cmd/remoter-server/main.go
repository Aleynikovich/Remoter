// Command remoter-server runs the relay and serves the web viewer.
package main

import (
	"flag"
	"io/fs"
	"log"
	"net/http"

	"github.com/aleynikovich/remoter/internal/relay"
	"github.com/aleynikovich/remoter/web"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	static, err := fs.Sub(web.Files, "static")
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/ws/", relay.New().Handler())
	mux.Handle("/", http.FileServerFS(static))

	log.Printf("relay listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
