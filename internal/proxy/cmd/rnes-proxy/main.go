// Command rnes-proxy is the sandbox's egress proxy. It is not meant to be run
// directly by a person: rnes builds it into a small image and starts it as a
// container for the lifetime of one sandbox session.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/df3l0p/rnes/internal/proxy"
)

func main() {
	addr := flag.String("addr", ":8888", "address to listen on")
	policyPath := flag.String("policy", "/etc/rnes-proxy/policy.txt", "path to the allow-list policy file (one host:port per line)")
	intercept := flag.Bool("intercept", false, "terminate TLS for the hosts the policy lists explicitly, with a CA created for this run, and log each request")
	caOut := flag.String("ca-out", "/ca/ca.pem", "with -intercept: where to write the trust bundle (the system roots plus the session CA's certificate; the CA's key is never written)")
	systemRoots := flag.String("system-roots", "/etc/ssl/certs/ca-certificates.crt", "with -intercept: the system trust roots to include in the bundle")
	providersStdin := flag.Bool("providers-stdin", false, "with -intercept: read one line of provider config (credentials to inject) from stdin before listening")
	forward := flag.String("forward", "", "with the sandbox's web UI: a passive TCP forward, as listen-target (e.g. :4097=sandbox:4096); it only dials the target for accepted connections")
	flag.Parse()
	if *providersStdin && !*intercept {
		log.Fatal("rnes-proxy: -providers-stdin needs -intercept: credentials can only be injected into decrypted requests")
	}

	pol, err := proxy.LoadPolicy(*policyPath)
	if err != nil {
		log.Fatalf("rnes-proxy: %v", err)
	}
	srv := &proxy.Server{Policy: pol}
	if *intercept {
		ca, bundle, err := proxy.TrustBundle(pol, *systemRoots)
		if err != nil {
			log.Fatalf("rnes-proxy: %v", err)
		}
		// World-readable: the sandbox reads it as another user, and it holds
		// only public certificates.
		if err := os.WriteFile(*caOut, bundle, 0o644); err != nil {
			log.Fatalf("rnes-proxy: writing the trust bundle: %v", err)
		}
		if ca == nil {
			// Every allowed host skips inspection (e.g. an empty .rnes/policy.txt
			// with a cloud harness): the bundle is the system roots alone, so the
			// sandbox's SSL_CERT_FILE still validates real upstream certificates.
			fmt.Printf("rnes-proxy: no hosts to intercept; system roots written to %s\n", *caOut)
		} else {
			fmt.Printf("rnes-proxy: intercepting %v; trust bundle written to %s\n", pol.DNSHosts(), *caOut)
			srv.CA = ca
		}
		if *providersStdin {
			inj, err := proxy.InjectorFromStdin(bufio.NewReader(os.Stdin), pol, log.Printf)
			if err != nil {
				log.Fatalf("rnes-proxy: %v", err)
			}
			for _, p := range inj.Providers {
				var active []string
				for _, h := range p.Hosts() {
					if pol.Explicit(h) && !pol.SkipsInspect(h) {
						active = append(active, h)
					}
				}
				if len(active) > 0 {
					fmt.Printf("rnes-proxy: provider %s will be injected at %v\n", p.Name(), active)
				}
			}
			if srv.CA != nil {
				srv.OnRequest = inj.OnRequest
			}
		}
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("rnes-proxy: %v", err)
	}
	if *forward != "" {
		if err := startForward(*forward); err != nil {
			log.Fatalf("rnes-proxy: %v", err)
		}
	}
	// rnes waits for this line before starting the sandbox, so print it only
	// once the port is actually bound (and, with -intercept, the bundle is
	// written, which it is by now).
	fmt.Printf("rnes-proxy: listening on %s\n", ln.Addr())
	log.Fatal(http.Serve(ln, srv))
}

// startForward runs the web UI's forwarder. spec is listen-target: it listens
// on listen inside the proxy container and dials target for every accepted
// connection. It is passive: the sandbox can't use it to reach anything, and
// only the published loopback port can reach it.
func startForward(spec string) error {
	listen, target, ok := strings.Cut(spec, "=")
	if !ok || listen == "" || target == "" {
		return fmt.Errorf("-forward wants listen=target, got %q", spec)
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("forward listener: %w", err)
	}
	fmt.Printf("rnes-proxy: forwarding %s to %s\n", ln.Addr(), target)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				up, err := net.DialTimeout("tcp", target, 10*time.Second)
				if err != nil {
					return
				}
				defer up.Close()
				go func() { io.Copy(up, conn); up.Close() }()
				io.Copy(conn, up)
			}()
		}
	}()
	return nil
}
