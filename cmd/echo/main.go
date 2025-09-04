package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "9001"
	}
	addr := ":" + port

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "echo %s %s (served by %s)\n", r.Method, r.URL.Path, addr)
	})

	log.Printf("echo listening on %s\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
