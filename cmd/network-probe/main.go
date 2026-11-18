// network-probe is a credential-free, test-only network diagnostic. It is not
// part of the database command protocol and is invoked by the trusted harness.
package main

import (
	"context"
	"net"
	"os"
	"strconv"
	"time"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "listen" {
		l, e := net.Listen("tcp4", ":9099")
		if e != nil {
			os.Exit(2)
		}
		defer l.Close()
		timer := time.AfterFunc(150*time.Second, func() { os.Exit(0) })
		defer timer.Stop()
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			c.Close()
		}
	}
	if len(os.Args) != 3 {
		os.Exit(2)
	}
	port, e := strconv.Atoi(os.Args[2])
	if e != nil || port < 1 || port > 65535 {
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	d := net.Dialer{}
	c, e := d.DialContext(ctx, "tcp4", net.JoinHostPort(os.Args[1], os.Args[2]))
	if e != nil {
		_, _ = os.Stdout.WriteString("blocked\n")
		return
	}
	c.Close()
	_, _ = os.Stdout.WriteString("reachable\n")
}
