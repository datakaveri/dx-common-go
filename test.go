package main

import (
	"log"
	"net/http"
	"os"

	"github.com/datakaveri/dx-common-go/openapi"
	"github.com/go-chi/chi/v5"
)

func main() {
	specPath := "./spec.yaml"
	if len(os.Args) >= 2 {
		specPath = os.Args[1]
	}

	loader, err := openapi.NewLoader(specPath)
	if err != nil {
		log.Fatalf("OpenAPI spec INVALID: %v", err)
	}

	b := openapi.NewRouterBuilder(loader)
	b.Operation("getHealth").Handle(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	}))
	b.Operation("getReady").Handle(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ready"))
	}))

	r := chi.NewRouter()
	r.Mount("/", b.Handler()) // all routing comes from spec + operationId links
	log.Fatal(http.ListenAndServe(":8080", r))
}
