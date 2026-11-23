package inspect

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestHTTPSManagedTransactionRecoversWithoutChangingBase(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("managed HTTPS state is owned by root")
	}
	root := t.TempDir()
	entry := httpsEntry{filepath.Join(root, "proxy"), filepath.Join(root, "tgcloud"), "nginx:1.30.5-alpine", "certbot/certbot:v5.8.0"}
	const domain = "files.example.test"
	previous := newHTTPSState()
	if err := requireHTTPSDir(entry.root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := entry.commit(previous); err != nil {
		t.Fatal(err)
	}
	failIssue, nginx, certbot := true, false, false
	var calls []string
	containers := func(service string) ([]byte, error) {
		role, network, state, rw := "reverse-proxy", "host", nginx, false
		paths := map[string]string{"/var/www/certbot": "certbot/www", "/etc/letsencrypt": "certbot/letsencrypt"}
		if service == "nginx" {
			paths["/etc/nginx/nginx.conf"] = "nginx/nginx.conf"
			paths["/etc/nginx/conf.d"] = "nginx/conf.d"
		} else {
			role, network, state, rw = "certificate-manager", httpsProject+"_default", certbot, true
			paths["/var/lib/letsencrypt"] = "certbot/lib"
			paths["/var/log/letsencrypt"] = "certbot/log"
			paths["/status"] = "certbot/status"
		}
		item := httpsContainer{Name: "/" + httpsProject + "-" + service, ID: strings.Repeat("a", 64),
			Image: "sha256:" + strings.Repeat("a", 64), Running: state, Network: network, Labels: map[string]string{
				"com.tgcloud.managed": "true", "com.tgcloud.role": role,
				"com.docker.compose.project": httpsProject, "com.docker.compose.service": service,
				"com.docker.compose.project.working_dir":  entry.root,
				"com.docker.compose.project.config_files": filepath.Join(entry.root, "docker-compose.yml"),
			}}
		for destination, relative := range paths {
			item.Mounts = append(item.Mounts, lanMount{"bind", filepath.Join(entry.root, relative), destination, &rw})
		}
		return json.Marshal(item)
	}
	docker := func(args ...string) ([]byte, error) {
		switch args[0] {
		case "run":
			return nil, nil // Offline syntax-check fixture.
		case "compose":
			if args[2] != entry.root {
				t.Fatalf("HTTPS called another Compose project: %v", args)
			}
			action := args[len(entry.compose()):]
			calls = append(calls, strings.Join(action, " "))
			switch action[0] {
			case "up":
				if action[len(action)-1] == "nginx" {
					nginx = true
				} else if action[len(action)-1] == "certbot" {
					certbot = true
				} else {
					t.Fatalf("HTTPS started an unrelated service: %v", action)
				}
			case "stop":
				nginx, certbot = false, false
			case "run":
				if failIssue {
					return nil, errors.New("fixture issuance failed")
				}
				writeHTTPSCertFixture(t, entry, domain)
			}
		case "container":
			if args[1] == "ls" {
				if slices.Contains(args, "--filter") {
					return []byte(httpsNginx + "|running\n" + httpsCertbot + "|running"), nil
				}
				return []byte(httpsNginx + "\n" + httpsCertbot), nil
			}
			return containers(strings.TrimPrefix(args[len(args)-1], httpsProject+"-"))
		case "image":
			return []byte(`{"os":"linux","arch":"amd64"}`), nil
		case "exec":
			if args[2] != "nginx" {
				t.Fatalf("unexpected exec during HTTPS recovery: %v", args)
			}
		default:
			t.Fatalf("unexpected Docker operation: %v", args)
		}
		return nil, nil
	}
	succeed := func(_ context.Context, _ string, proxy httpsProxy, _ DockerRunner) map[string]string {
		if proxy.nginx == nil || !proxy.nginx.Running || proxy.certbot == nil || !proxy.certbot.Running {
			t.Fatal("proxy checked before both services started")
		}
		markers := map[string]string{}
		for _, key := range []string{"CORE", "PORTS", "NGINX", "CERTBOT", "RENEWAL", "NGINX_TEST", "HTTPS", "REDIRECT", "DAV_BLOCK", "BACKEND_LOCAL", "CERT"} {
			markers["PROXY_"+key] = "OK"
		}
		return markers
	}
	var progress bytes.Buffer
	configure := func(state httpsState) error {
		return entry.configure(context.Background(), state, domain, "", "amd64", docker, docker, succeed, &progress)
	}

	if err := configure(previous); err == nil || nginx || certbot {
		t.Fatalf("issuance failure did not stop the bootstrap proxy: error=%v nginx=%v certbot=%v", err, nginx, certbot)
	}
	if state, _, err := entry.readState(); err != nil || state.domain() != "" {
		t.Fatalf("failed issuance changed the prior state: %+v %v", state, err)
	}
	if _, err := os.Lstat(filepath.Join(entry.root, "nginx/conf.d/10-acme-bootstrap.conf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ACME-only route was not cleared: %v", err)
	}
	failIssue = false
	progress.Reset()
	if err := configure(previous); err != nil {
		t.Fatalf("retry failed: %v\n%s", err, progress.String())
	}
	configured, _, err := entry.readState()
	if err != nil || configured.domain() != domain || !strings.Contains(progress.String(), "TGCLOUD_HTTPS=OK") {
		t.Fatalf("successful verification did not commit the route: %+v %v", configured, err)
	}

	calls = nil
	if err := configure(configured); err != nil {
		t.Fatal(err)
	}
	for _, call := range calls {
		if strings.HasPrefix(call, "run ") {
			t.Fatalf("a usable certificate was reissued: %s", call)
		}
	}
	if _, err := os.Stat(entry.directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("HTTPS created files in the base installation directory: %v", err)
	}
}

func writeHTTPSCertFixture(t *testing.T, entry httpsEntry, domain string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{domain},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(30 * 24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(entry.root, "certbot/letsencrypt")
	for _, dir := range []string{"live/" + domain, "archive/" + domain, "renewal"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string][]byte{
		"archive/" + domain + "/fullchain1.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		"archive/" + domain + "/privkey1.pem":   []byte("fixture key"),
		"renewal/" + domain + ".conf":           []byte("fixture renewal"),
	} {
		if err := os.WriteFile(filepath.Join(root, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"fullchain", "privkey"} {
		if err := os.Symlink("../../archive/"+domain+"/"+name+"1.pem", filepath.Join(root, "live", domain, name+".pem")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHTTPSPortAndDNSPreflight(t *testing.T) {
	ports, err := publishedHTTPSPorts(func(...string) ([]byte, error) {
		return []byte("0.0.0.0:79-80->79-80/tcp,0.0.0.0:443->443/udp"), nil
	})
	if err != nil || !ports[80] || ports[443] {
		t.Fatalf("expected only the published HTTPS TCP listener to conflict: %v, %v", ports, err)
	}
	own := map[string]bool{"12": true}
	if got := classifyHTTPSListener("LISTEN users:((\"nginx\",pid=12,fd=6))\nLISTEN unknown", true, own); got != "UNKNOWN" {
		t.Fatalf("unobservable listener was accepted: %s", got)
	}
	if got := classifyHTTPSListener("LISTEN users:((\"nginx\",pid=12,fd=6))\nLISTEN users:((\"other\",pid=13,fd=6))", true, own); got != "FOREIGN" {
		t.Fatalf("foreign listener was accepted: %s", got)
	}
	environment := httpsEnvironment{Port80: "FREE", Port443: "FREE", DNSA: []string{"8.8.8.8"}, Local: []string{"192.168.26.5"}}
	if environment.safe("") == nil || environment.safe("8.8.8.8") != nil {
		t.Fatal("NAT requires a matching confirmation")
	}
	environment.DNSAAAA = []string{"2001:db8::1"}
	if environment.safe("8.8.8.8") == nil {
		t.Fatal("NAT confirmation allowed an unmatched AAAA record")
	}
}
