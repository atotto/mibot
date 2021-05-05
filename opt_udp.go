package main

import (
	"net"
	"strconv"
	"strings"
)

func optUDPConn(s string) (*net.UDPConn, error) {
	addr := &net.UDPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: 8823,
	}

	if opt := strings.TrimPrefix(s, "udp://"); opt != "" {
		host, p, err := net.SplitHostPort(opt)
		if err != nil {
			return nil, err
		}
		port, err := strconv.Atoi(p)
		if err != nil {
			return nil, err
		}

		addr = &net.UDPAddr{
			IP:   net.ParseIP(host),
			Port: port,
		}
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, err
	}
	if err := conn.SetReadBuffer(256); err != nil {
		return nil, err
	}
	return conn, nil
}
