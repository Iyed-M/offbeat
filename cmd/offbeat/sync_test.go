package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Iyed-M/offbeat/internal/ipc"
	"github.com/Iyed-M/offbeat/internal/lansync"
)

func TestCLISyncProvisioning(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "offbeat-lan-cli-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	configPath := writeCLIAdapterConfig(t, home)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	configFile, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(configFile, "\n[sync]\nhttps_port=%d\nlan_bind_address='127.0.0.1'\n", port)
	configFile.Close()
	daemon := exec.Command(offbeatdPath)
	daemon.Env = append(os.Environ(), "OFFBEAT_HOME="+home, "OFFBEAT_ADAPTER_CREDENTIAL=test-adapter-credential")
	var stderr bytes.Buffer
	daemon.Stderr = &stderr
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { daemon.Process.Signal(os.Interrupt); daemon.Wait() }()
	if err := waitForDaemonReady(home, 5*time.Second); err != nil {
		t.Fatalf("%v: %s", err, stderr.String())
	}
	out, errOut, err := runCLI(t, home, "sync", "setup")
	if err != nil {
		t.Fatalf("%v: %s", err, errOut)
	}
	var setup ipc.SyncSetupResult
	if err := json.Unmarshal([]byte(out), &setup); err != nil {
		t.Fatalf("setup output must be provisionable JSON: %s", out)
	}
	client, err := lansync.NewClient(setup.Address, setup.CertificateSHA256, setup.Credential)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Manifest(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"sync", "status"}, {"status"}, {"config"}} {
		out, errOut, err := runCLI(t, home, args[0], args[1:]...)
		if err != nil {
			t.Fatalf("%v %s", err, errOut)
		}
		if strings.Contains(out, setup.Credential) {
			t.Fatalf("secret in %v", args)
		}
	}
	out, errOut, err = runCLI(t, home, "sync", "reset")
	if err != nil {
		t.Fatalf("%v %s", err, errOut)
	}
	var reset ipc.SyncSetupResult
	if json.Unmarshal([]byte(out), &reset) != nil || reset.Credential == setup.Credential {
		t.Fatal("reset did not provision a new credential")
	}
	if _, err := client.Manifest(context.Background()); err == nil {
		t.Fatal("CLI reset did not revoke phone")
	}
	logBytes, err := os.ReadFile(filepath.Join(home, ".local/state/offbeat/log/offbeatd.log"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(logBytes, []byte(setup.Credential)) || bytes.Contains(logBytes, []byte(reset.Credential)) {
		t.Fatal("provisioning secrets logged")
	}
	if _, _, err := runCLI(t, home, "sync", "setup", "extra"); err == nil {
		t.Fatal("invalid setup arguments accepted")
	}
}
