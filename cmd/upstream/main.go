package main

// this is just echo servers for backend
import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "9001"
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello from %s %s\n", port, r.URL.Path)
	})
	_ = http.ListenAndServe(":"+port, nil)
}
