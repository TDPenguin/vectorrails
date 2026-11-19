package main

import (
	"flag"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "listen address")
	upstream := flag.String("upstream", "https://openrailwaymap.app", "upstream tile server")
	fixBools := flag.Bool("fixbools", true, "rewrite true/false values in tiles as the text \"true\"/\"false\"")
	flag.Parse()

	client := &http.Client{Timeout: 60 * time.Second}
	base := strings.TrimRight(*upstream, "/")

	http.HandleFunc("/signals/", newSignalsHandler(client, base, loadSprite(client, base)))

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		url := base + r.URL.Path

		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		req.Header.Set("Accept", "application/x-protobuf")
		setHeaders(req)

		resp, err := client.Do(req)
		if err != nil {
			log.Printf("ERR  %s: %v", r.URL.Path, err)
			http.Error(w, "upstream error", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Printf("ERR  %s: reading body: %v", r.URL.Path, err)
			http.Error(w, "upstream read error", http.StatusBadGateway)
			return
		}
		received := len(body)

		// rewrite true/false values as text, nly for a successful MVT tile on any problem the tile
		// is sent through unmodified
		if *fixBools && resp.StatusCode == http.StatusOK && len(body) > 0 && body[0] == 0x1a {
			fixed, _, err := boolsToText(body)
			if err != nil {
				log.Printf("WARN %s: bool rewrite failed, sending tile untouched: %v", r.URL.Path, err)
			} else {
				body = fixed
			}
		}

		// log per success
		log.Printf("%d  %s  %s  %d bytes",
			resp.StatusCode, r.URL.Path, resp.Header.Get("Content-Type"), received)

		if ct := resp.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		}

		w.WriteHeader(resp.StatusCode)
		w.Write(body)
	})

	log.Printf("listening on http://%s, forwarding to %s", *listen, base)
	log.Fatal(http.ListenAndServe(*listen, nil))
}
