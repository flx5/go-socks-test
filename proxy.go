package main

import (
	"context"
	"fmt"
	"github.com/armon/go-socks5"
	"golang.org/x/net/proxy"
	"io"
	"net"
)

func main() {
	testSocksForwarding("localhost", "root", "Muster!")
}

func testSocksForwarding(host string, username, password string) {
	sshPort := 39795
	//apiPort := 42439

	sshClient, err := connectSSH(username, password, host, sshPort)
	if err != nil {
		panic(err)
	}
	defer sshClient.Close()

	server, err := setupProxyServer(sshDialer(sshClient))
	if err != nil {
		panic(err)
	}

	socksListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	defer socksListener.Close()

	go server.Serve(socksListener)

	proxyAddress := fmt.Sprintf("socks5://%s", socksListener.Addr().String())

	testHttp("http://127.0.0.1", proxyAddress)
	/*testSSH("127.0.0.1:22", socksListener.Addr().String(), username, password)
	testVNC(socksListener.Addr().String(), host, apiPort, username, password)*/
	testForwarding(socksListener.Addr().String())
}

func testForwarding(proxyAddress string) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")

	if err != nil {
		panic(err)
	}

	fmt.Printf("Forwarder listening on %s\n", listener.Addr())

	for {
		accept, err := listener.Accept()
		if err != nil {
			fmt.Printf("Error: %v", err)
			continue
		}

		go handleConnection(proxyAddress, accept)
	}
}

func handleConnection(proxyAddress string, accept net.Conn) {
	defer accept.Close()
	conn, err := ProxyConnectFunc(proxyAddress, "tcp", "127.0.0.1:8080")()

	if err != nil {
		fmt.Printf("[FORWARD] Connect proxy Error: %v", err)
		return
	}

	defer conn.Close()

	txDone := make(chan struct{})
	rxDone := make(chan struct{})

	go func() {
		_, err := io.Copy(conn, accept)

		// Close conn so that other copy operation unblocks
		conn.Close()
		close(txDone)
		fmt.Printf("[FORWARD] tx done")

		if err != nil {
			fmt.Printf("[FORWARD] Error conn <- accept: %v", err)
			return
		}
	}()

	go func() {
		_, err := io.Copy(accept, conn)

		// Close accept so that other copy operation unblocks
		accept.Close()
		close(rxDone)
		fmt.Printf("[FORWARD] rx done")

		if err != nil {
			fmt.Printf("[FORWARD] Error accept <- conn: %v", err)
			return
		}
	}()

	<-txDone
	<-rxDone

	fmt.Printf("Handle connection done", err)
}

func ProxyConnectFunc(socksProxy string, network, addr string) func() (net.Conn, error) {
	return func() (net.Conn, error) {
		// create a socks5 dialer
		dialer, err := proxy.SOCKS5("tcp", socksProxy, nil, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("Can't connect to the proxy: %s", err)
		}

		c, err := dialer.Dial(network, addr)
		if err != nil {
			return nil, err
		}

		return c, nil
	}
}

func setupProxyServer(dialer func(ctx context.Context, network, addr string) (net.Conn, error)) (*socks5.Server, error) {
	socksConfig := &socks5.Config{
		Dial: dialer,
	}
	server, err := socks5.New(socksConfig)
	if err != nil {
		return nil, fmt.Errorf("could not setup socks server: %w", err)
	}

	return server, nil
}
