package main

import (
	"bytes"
	"context"
	"fmt"
	"golang.org/x/crypto/ssh"
	"log"
	"net"
	"time"
)

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

func sshDialer(client *ssh.Client) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return client.Dial("tcp", addr)
	}
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
