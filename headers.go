package main

import "net/http"

const (
	refererHeader   = "https://openrailwaymap.app/"
	userAgentHeader = "NIMBYRails"
)

func setHeaders(req *http.Request) {
	req.Header.Set("Referer", refererHeader)
	req.Header.Set("User-Agent", userAgentHeader)
}
