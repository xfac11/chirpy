package main

import (
	"log"
	"net/http"
)

func healthzHandler(responseWrite http.ResponseWriter, request *http.Request) {
	responseWrite.Header().Add("Content-Type", "text/plain; charset=utf-8")
	responseWrite.WriteHeader(http.StatusOK)

	var body = []byte("OK")
	responseWrite.Write(body)
}
func main() {
	fileServer := http.StripPrefix("/app", http.FileServer(http.Dir(".")))

	serveMux := http.NewServeMux()
	serveMux.Handle("GET /app/", fileServer)
	serveMux.HandleFunc("/healthz", healthzHandler)

	server := http.Server{
		Addr:    ":8080",
		Handler: serveMux,
	}

	log.Fatal(server.ListenAndServe())
}
