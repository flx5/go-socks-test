package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/armon/go-socks5"
	"github.com/mitchellh/go-vnc"
	xenapi "github.com/terra-farm/go-xen-api-client"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/proxy"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func main() {
	testSocksForwarding("localhost", "root", "Muster!")
}

func testSocksForwarding(host string, username, password string) {
	sshPort := 42089
	apiPort := 42439

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
	testSSH("127.0.0.1:22", socksListener.Addr().String(), username, password)
	testVNC(socksListener.Addr().String(), host, apiPort, username, password)
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

func getHostConsoleLocation(xapi *xenapi.Client, session xenapi.SessionRef) (string, error) {
	/*
		Host console uses RFB3.3 which is not supported by go-vnc

		host, err := xapi.Session.GetThisHost(session, session)
		if err != nil {
			panic(err)
		}

		vm, err := xapi.Host.GetControlDomain(session, host)
		if err != nil {
			panic(err)
		}*/

	vm, _ := xapi.VM.GetByNameLabel(session, "Other install media (1)")

	consoles, err := xapi.VM.GetConsoles(session, vm[0])
	if err != nil {
		panic(err)
	}

	for _, console := range consoles {
		consoleRecord, err := xapi.Console.GetRecord(session, console)
		if err != nil {
			panic(err)
		}

		fmt.Printf("Found console with protocol %s\n", consoleRecord.Protocol)

		if consoleRecord.Protocol == xenapi.ConsoleProtocolRfb {
			return consoleRecord.Location, nil
		}
	}

	return "", errors.New("could not find console on hypervisor.")
}

func testVNC(proxyAddress, host string, apiPort int, username, password string) {
	xapi, err := xenapi.NewClient(fmt.Sprintf("https://%s:%d/", host, apiPort), nil)
	if err != nil {
		panic(err)
	}

	session, err := xapi.Session.LoginWithPassword(username, password, "1.0", "example")
	if err != nil {
		panic(err)
	}

	location, err := getHostConsoleLocation(xapi, session)

	if err != nil {
		panic(err)
	}

	fmt.Printf("Found console at %s\n", location)

	parsedUrl, err := url.Parse(location)
	if err != nil {
		panic(err)
	}

	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
	}

	var target string
	if parsedUrl.Port() != "" {
		target = parsedUrl.Host
	} else if parsedUrl.Scheme == "http" {
		target = parsedUrl.Host + ":80"
	} else if parsedUrl.Scheme == "https" {
		target = parsedUrl.Host + ":443"
	} else {
		panic("Unsupported protocol")
	}

	fmt.Printf("Connecting to %s %s\n", parsedUrl.Host, parsedUrl.Port())

	proxyConnection, err := ProxyConnectFunc(proxyAddress, "tcp", target)()

	if err != nil {
		panic(err)
	}

	defer proxyConnection.Close()

	tlsConnection := tls.Client(proxyConnection, tlsConfig)

	request, err := http.NewRequest(http.MethodConnect, location, http.NoBody)
	if err != nil {
		panic(err)
	}

	request.Close = false

	request.AddCookie(&http.Cookie{
		Name:  "session_id",
		Value: string(session),
	})

	err = request.Write(tlsConnection)
	if err != nil {
		panic(err)
	}

	// Look for \r\n\r\n sequence. Everything after the HTTP Header is for the vnc client.
	response, err := readHttpHeader(tlsConnection)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Received response: %s", response)

	vncClient, err := vnc.Client(tlsConnection, &vnc.ClientConfig{
		Exclusive: false,
	})
	if err != nil {
		panic(err)
	}

	defer vncClient.Close()

	err = xapi.Session.Logout(session)
	if err != nil {
		panic(err)
	}
}

func parseProtocolVersion(pv []byte) (uint, uint, error) {
	var major, minor uint

	l, err := fmt.Sscanf(string(pv), "RFB %d.%d\n", &major, &minor)
	if l != 2 {
		return 0, 0, fmt.Errorf("error parsing ProtocolVersion.")
	}
	if err != nil {
		return 0, 0, err
	}

	return major, minor, nil
}

func readHttpHeader(tlsConn *tls.Conn) (string, error) {
	builder := strings.Builder{}
	buffer := make([]byte, 1)
	sequenceProgress := 0

	for {
		if _, err := io.ReadFull(tlsConn, buffer); err != nil {
			return "", fmt.Errorf("failed to start vnc session: %w", err)
		}

		builder.WriteByte(buffer[0])

		if buffer[0] == '\n' && sequenceProgress%2 == 1 {
			sequenceProgress++
		} else if buffer[0] == '\r' && sequenceProgress%2 == 0 {
			sequenceProgress++
		} else {
			sequenceProgress = 0
		}

		if sequenceProgress == 4 {
			break
		}
	}
	return builder.String(), nil
}

func testSSH(host, proxyAddress, username, password string) {
	connection, err := ProxyConnectFunc(proxyAddress, "tcp", host)()

	if err != nil {
		panic(err)
	}

	defer connection.Close()

	config := &ssh.ClientConfig{
		User: username,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	connectionEstablished := make(chan struct{}, 1)
	var sshConn ssh.Conn
	var sshChan <-chan ssh.NewChannel
	var req <-chan *ssh.Request

	go func() {
		sshConn, sshChan, req, err = ssh.NewClientConn(connection, host, config)
		close(connectionEstablished)
	}()

	timeout := time.Minute

	select {
	case <-connectionEstablished:
		// We don't need to do anything here. We just want select to block until
		// we connect or timeout.
	case <-time.After(timeout):
		if sshConn != nil {
			sshConn.Close()
		}

		panic("TIMEOUT")
	}

	defer sshConn.Close()

	sshClient := ssh.NewClient(sshConn, sshChan, req)
	defer sshClient.Close()
	session, err := sshClient.NewSession()
	if err != nil {
		panic(err)
	}
	defer session.Close()

	var b bytes.Buffer
	session.Stdout = &b
	if err := session.Run("/usr/bin/whoami"); err != nil {
		log.Fatal("Failed to run: " + err.Error())
	}
	fmt.Println(b.String())
}

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

func sshDialer(client *ssh.Client) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return client.Dial("tcp", addr)
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

func connectSSH(username string, password string, host string, port int) (*ssh.Client, error) {
	config := &ssh.ClientConfig{
		User: username,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	client, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", host, port), config)
	if err != nil {
		return nil, fmt.Errorf("could not connect to ssh server: %w", err)
	}
	return client, err
}
