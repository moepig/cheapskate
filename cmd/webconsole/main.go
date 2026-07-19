// webconsole serves the opt-in browser frontend. On Lambda (behind the IP-restricted API Gateway REST API) it speaks the runtime API via algnhsa; run locally it is a plain HTTP server bound to localhost.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"time"
	_ "time/tzdata"

	"github.com/akrylysov/algnhsa"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"cheapskate/internal/store"
	"cheapskate/internal/webconsole"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address for local (non-Lambda) mode")
	flag.Parse()

	table := os.Getenv("STATE_TABLE_NAME")
	if table == "" {
		table = os.Getenv("CHEAPSKATE_TABLE")
	}
	if table == "" {
		log.Fatal("STATE_TABLE_NAME (or CHEAPSKATE_TABLE) is required")
	}
	loc := time.Local
	if tz := os.Getenv("DEFAULT_TIMEZONE"); tz != "" {
		var err error
		if loc, err = time.LoadLocation(tz); err != nil {
			log.Fatalf("invalid DEFAULT_TIMEZONE %q: %v", tz, err)
		}
	}

	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("load AWS config: %v", err)
	}
	s := store.New(dynamodb.NewFromConfig(cfg), table)
	// BASE_PATH is the browser-visible prefix (the API Gateway stage, e.g. "/console"); the Lambda proxy event paths themselves arrive without it.
	handler := webconsole.New(s, os.Getenv("BASE_PATH"), loc).Handler()

	if os.Getenv("AWS_LAMBDA_RUNTIME_API") != "" {
		algnhsa.ListenAndServe(handler, nil)
		return
	}
	log.Printf("webconsole listening on http://%s/", *addr)
	log.Fatal(http.ListenAndServe(*addr, handler))
}
