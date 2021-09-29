package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/mitchellh/go-vnc"
	xenapi "github.com/terra-farm/go-xen-api-client"
	"io"
	"net/http"
	"net/url"
	"strings"
)

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
