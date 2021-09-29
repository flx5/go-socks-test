package main

import (
	"fmt"
	"net/http"
	"net/url"
)

func testHttp(link, proxyAddress string) {
	proxy_url, err := url.Parse(proxyAddress)
	if err != nil {
		panic(err)
	}

	transport := &http.Transport{
		Proxy: http.ProxyURL(proxy_url),
	}

	client := &http.Client{
		Transport: transport,
	}
	response, err := client.Get(link)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Response: %s\n", response.Status)
}
