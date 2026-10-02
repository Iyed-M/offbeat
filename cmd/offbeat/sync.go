package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/Iyed-M/offbeat/internal/app"
	"github.com/Iyed-M/offbeat/internal/ipc"
)

func runSync(configPath, homeDir, operation string) int {
	bootstrap, err := loadBootstrapConfig(configPath, homeDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "offbeat sync: could not locate Control socket")
		return 1
	}
	response, err := requestControl(app.SocketPath(bootstrap.SocketDir), "sync."+operation)
	if err != nil {
		fmt.Fprintln(os.Stderr, "offbeat sync: daemon unavailable")
		return 1
	}
	if response.Error != nil {
		fmt.Fprintf(os.Stderr, "offbeat sync: %s: %s\n", response.Error.Code, response.Error.Message)
		return 1
	}
	if response.Version != ipc.ProtocolVersion {
		fmt.Fprintln(os.Stderr, "offbeat sync: unsupported daemon reply")
		return 1
	}
	data, err := ipc.Encode(response.Result)
	if err != nil {
		return 1
	}
	var result any
	if operation == "status" {
		var status ipc.SyncStatusResult
		if ipc.Decode(data, &status) != nil {
			fmt.Fprintln(os.Stderr, "offbeat sync: invalid status reply")
			return 1
		}
		result = status
	} else {
		var setup ipc.SyncSetupResult
		if ipc.Decode(data, &setup) != nil || setup.Address == "" || len(setup.CertificateSHA256) != 64 || len(setup.Credential) != 43 {
			fmt.Fprintln(os.Stderr, "offbeat sync: invalid provisioning reply")
			return 1
		}
		result = setup
	}
	// setup/reset are explicitly private provisioning output; no credential is
	// printed by status or any automatic diagnostic path.
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if encoder.Encode(result) != nil {
		return 1
	}
	return 0
}
